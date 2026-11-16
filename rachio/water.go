package rachio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// The longest durations the documentation gives for a command. Its ranges
// start at zero; a zone run and a pause of no time are refused here.
const (
	// MaxZoneRun is the longest a zone is started for.
	MaxZoneRun = 3 * time.Hour
	// MaxRainDelay is the longest rain delay.
	MaxRainDelay = 7 * 24 * time.Hour
	// MaxPause is the longest a run is paused for.
	MaxPause = time.Hour
)

// Run is what a controller is watering now.
type Run struct {
	// ScheduleID is the schedule that runs, when one does.
	ScheduleID string
	// Type and Status are Rachio's words; the documentation lists neither
	// and shows MANUAL and PROCESSING.
	Type   string
	Status string
	// Started and Duration are the whole run's.
	Started  time.Time
	Duration time.Duration
	// ZoneID is the zone watering, with its own start and duration.
	ZoneID       string
	ZoneStarted  time.Time
	ZoneDuration time.Duration
}

// Event is one line of a controller's history.
type Event struct {
	ID string
	// Category and Type are Rachio's words: the documentation shows the
	// category SCHEDULE and the types SCHEDULE_STATUS and ZONE_STATUS.
	Category string
	Type     string
	At       time.Time
	// Summary is the event in Rachio's sentence.
	Summary string
}

// ZoneRun is one zone to start and for how long.
type ZoneRun struct {
	ZoneID   string
	Duration time.Duration
}

type (
	wireRun struct {
		ScheduleID    string `json:"scheduleId"`
		Type          string `json:"type"`
		Status        string `json:"status"`
		StartDate     int64  `json:"startDate"`
		Duration      int64  `json:"duration"`
		ZoneID        string `json:"zoneId"`
		ZoneStartDate int64  `json:"zoneStartDate"`
		ZoneDuration  int64  `json:"zoneDuration"`
	}
	wireEvent struct {
		ID        string `json:"id"`
		Category  string `json:"category"`
		Type      string `json:"type"`
		EventDate int64  `json:"eventDate"`
		Summary   string `json:"summary"`
	}
	wireZoneRun struct {
		ID        string `json:"id"`
		Duration  int64  `json:"duration"`
		SortOrder int    `json:"sortOrder"`
	}
	// The documentation's schema names this key zoneRunDurations and its
	// sample request sends zones; the sample is what is sent.
	wireZoneRuns struct {
		Zones []wireZoneRun `json:"zones"`
	}
)

// Current is what the controller waters now, and whether it waters at
// all. The documentation does not say what the answer is when nothing
// runs: an answer with no status in it, an empty one among them, is read
// as nothing.
func (c *Client) Current(ctx context.Context, device string) (Run, bool, error) {
	path, err := record("device", device, "/current_schedule")
	if err != nil {
		return Run{}, false, err
	}

	raw, err := c.call(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return Run{}, false, err
	}

	if len(bytes.TrimSpace(raw)) == 0 {
		return Run{}, false, nil
	}

	var wire wireRun
	if err = json.Unmarshal(raw, &wire); err != nil {
		return Run{}, false, fmt.Errorf("%w: GET %s: %w", ErrAnswer, path, err)
	}

	if wire.Status == "" {
		return Run{}, false, nil
	}

	return Run{
		ScheduleID:   wire.ScheduleID,
		Type:         wire.Type,
		Status:       wire.Status,
		Started:      stamp(wire.StartDate),
		Duration:     seconds(wire.Duration),
		ZoneID:       wire.ZoneID,
		ZoneStarted:  stamp(wire.ZoneStartDate),
		ZoneDuration: seconds(wire.ZoneDuration),
	}, true, nil
}

// Events is the controller's history from since to until, in the order
// Rachio gives it. The documentation gives no longest range.
func (c *Client) Events(ctx context.Context, device string, since, until time.Time) ([]Event, error) {
	path, err := record("device", device, "/event")
	if err != nil {
		return nil, err
	}

	if !since.Before(until) {
		return nil, fmt.Errorf("%w: events from %s to %s", ErrArgument, since, until)
	}

	query := url.Values{
		"startTime": {strconv.FormatInt(since.UnixMilli(), 10)},
		"endTime":   {strconv.FormatInt(until.UnixMilli(), 10)},
	}

	var wire []wireEvent
	if err = c.read(ctx, path, query, &wire); err != nil {
		return nil, err
	}

	events := make([]Event, 0, len(wire))
	for _, event := range wire {
		events = append(events, Event{
			ID:       event.ID,
			Category: event.Category,
			Type:     event.Type,
			At:       stamp(event.EventDate),
			Summary:  event.Summary,
		})
	}

	return events, nil
}

// StartZone waters one zone for length, a second to MaxZoneRun, in place
// of whatever the controller was watering.
func (c *Client) StartZone(ctx context.Context, zone string, length time.Duration) error {
	if err := within(length, time.Second, MaxZoneRun, "a zone run"); err != nil {
		return err
	}

	return c.command(ctx, "/zone/start", zone, &length)
}

// StartZones waters several zones one after the other, in the order
// given, each for its own length.
func (c *Client) StartZones(ctx context.Context, runs []ZoneRun) error {
	if len(runs) == 0 {
		return fmt.Errorf("%w: no zone to start", ErrArgument)
	}

	body := wireZoneRuns{Zones: make([]wireZoneRun, 0, len(runs))}

	for index, run := range runs {
		if run.ZoneID == "" {
			return fmt.Errorf("%w: an empty zone id", ErrArgument)
		}

		if err := within(run.Duration, time.Second, MaxZoneRun, "a zone run"); err != nil {
			return err
		}

		body.Zones = append(body.Zones, wireZoneRun{
			ID:        run.ZoneID,
			Duration:  int64(run.Duration / time.Second),
			SortOrder: index + 1,
		})
	}

	_, err := c.call(ctx, http.MethodPut, "/zone/start_multiple", nil, body)

	return err
}

// StopWater stops all watering on the controller.
func (c *Client) StopWater(ctx context.Context, device string) error {
	return c.command(ctx, "/device/stop_water", device, nil)
}

// RainDelay keeps the controller from watering for length, up to
// MaxRainDelay. The documentation allows zero and does not say that it
// ends a delay.
func (c *Client) RainDelay(ctx context.Context, device string, length time.Duration) error {
	if err := within(length, 0, MaxRainDelay, "a rain delay"); err != nil {
		return err
	}

	return c.command(ctx, "/device/rain_delay", device, &length)
}

// Standby puts the controller in standby, where it runs no schedule, or
// takes it out.
func (c *Client) Standby(ctx context.Context, device string, standby bool) error {
	if standby {
		return c.command(ctx, "/device/off", device, nil)
	}

	return c.command(ctx, "/device/on", device, nil)
}

// PauseRun pauses what the controller waters for length, up to MaxPause.
func (c *Client) PauseRun(ctx context.Context, device string, length time.Duration) error {
	if err := within(length, time.Second, MaxPause, "a pause"); err != nil {
		return err
	}

	return c.command(ctx, "/device/pause_zone_run", device, &length)
}

// ResumeRun resumes a paused run.
func (c *Client) ResumeRun(ctx context.Context, device string) error {
	return c.command(ctx, "/device/resume_zone_run", device, nil)
}

// StartSchedule runs a schedule now.
func (c *Client) StartSchedule(ctx context.Context, schedule string) error {
	return c.command(ctx, "/schedulerule/start", schedule, nil)
}

// SkipSchedule skips a schedule; the documentation does not say whether
// that is the run under way or the next one.
func (c *Client) SkipSchedule(ctx context.Context, schedule string) error {
	return c.command(ctx, "/schedulerule/skip", schedule, nil)
}
