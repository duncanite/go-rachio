package rachio_test

// The contract, from Rachio's documentation (https://rachio.readme.io),
// each rule under the page it is on. No answer has a schema there: the
// shapes are those of its examples.
//
// Authentication, /reference/authentication: every request carries
// "Authorization: Bearer <key>", the key the Rachio app gives.
//
// Rate limiting, /reference/rate-limiting: 3,500 requests a day; every
// answer carries X-RateLimit-Limit, X-RateLimit-Remaining and
// X-RateLimit-Reset. The page says the reset is written per RFC 1123 and
// shows "2025-01-03T00:00:00Z". It gives no status for a limit reached.
//
// The account: GET /person/info gives {"id"}; GET /person/:id gives the
// person with devices, each with zones (id, zoneNumber, name, enabled),
// scheduleRules and flexScheduleRules (id, name, externalName), status,
// on, paused, timeZone, and rainDelayExpirationDate in milliseconds since
// 1970. GET /device/:id gives one such device.
//
// GET /device/:id/current_schedule gives the schedule running "if any":
// scheduleId, type, status, startDate and zoneStartDate in milliseconds,
// duration and zoneDuration in seconds, zoneId.
//
// GET /device/:id/event?startTime=&endTime=, both "milliseconds unix
// epoch", gives a list: id, category, type, eventDate, summary.
//
// The commands are PUT with a JSON body and answer 204 with no body:
//   - /zone/start {"id", "duration"}: a zone, seconds, 0 to 10800.
//   - /zone/start_multiple {"zones": [{"id", "duration", "sortOrder"}]},
//     by the page's sample request; its schema names the key
//     zoneRunDurations.
//   - /device/stop_water {"id"}.
//   - /device/rain_delay {"id", "duration"}: seconds, 0 to 604800.
//   - /device/on and /device/off {"id"}.
//   - /device/pause_zone_run {"id", "duration"}: seconds, 0 to 3600;
//     /device/resume_zone_run {"id"}.
//   - /schedulerule/start and /schedulerule/skip {"id"}.
//
// The one error shown: 400 with {"error": "Missing data", "code": 301}.

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/duncanite/go-rachio/rachio"
	"github.com/duncanite/go-rachio/rachio/rachiotest"
)

func started(t *testing.T) (*rachiotest.API, *rachio.Client) {
	t.Helper()

	api := rachiotest.New()
	t.Cleanup(api.Close)

	return api, rachio.New(rachiotest.Key, rachio.Options{BaseURL: api.URL, HTTP: api.Client()})
}

// TestAccount: the account with its controller, the zones by their
// number, the fixed schedules then the flex ones, a schedule with no name
// under the one Rachio shows; whose key it is, asked once.
func TestAccount(t *testing.T) {
	t.Parallel()

	api, client := started(t)

	person, err := client.Person(t.Context())
	if err != nil {
		t.Fatalf("Person: %v", err)
	}

	want := rachio.Device{
		ID: rachiotest.DeviceID, Name: "Garden", Model: "GENERATION2_8ZONE", SerialNumber: "SERIAL0001",
		Status: "ONLINE", On: true, TimeZone: "America/Denver",
		Zones: []rachio.Zone{
			{ID: rachiotest.FrontID, Number: 1, Name: "Front Beds", Enabled: true},
			{
				ID: rachiotest.BackID, Number: 2, Name: "Back Lawn", Enabled: true,
				LastWatered: time.UnixMilli(rachiotest.LastWatered),
			},
			{ID: rachiotest.UnusedID, Number: 3, Name: "Zone 3"},
		},
		Schedules: []rachio.ScheduleRef{
			{ID: rachiotest.FixedID, Name: "Every Wed at 9:05 AM"},
			{ID: rachiotest.FlexID, Name: "Summer lawn", Flex: true},
		},
	}

	if person.ID != rachiotest.PersonID || person.Username != "owner" || person.FullName != "An Owner" ||
		person.Email != "owner@example.com" || len(person.Devices) != 1 ||
		!reflect.DeepEqual(person.Devices[0], want) || !person.Devices[0].Online() {
		t.Errorf("Person = %+v\nwant the device %+v", person, want)
	}

	api.Online, api.On = false, false
	api.RainDelayUntil = time.UnixMilli(1420027691501)

	device, err := client.Device(t.Context(), rachiotest.DeviceID)
	if err != nil || device.Online() || device.On || !device.RainDelayUntil.Equal(api.RainDelayUntil) {
		t.Errorf("Device = %+v, %v; want it offline, in standby, delayed until %s", device, err, api.RainDelayUntil)
	}

	if _, err = client.Person(t.Context()); err != nil {
		t.Fatalf("Person again: %v", err)
	}

	reads := []string{
		"person/info", "person/" + rachiotest.PersonID, "device/" + rachiotest.DeviceID,
		"person/" + rachiotest.PersonID,
	}
	if !slices.Equal(api.Reads, reads) {
		t.Errorf("the account was read as %v, want %v", api.Reads, reads)
	}
}

// TestCurrent: what waters now, its times and durations; nothing, when
// the answer is an empty object, or empty.
func TestCurrent(t *testing.T) {
	t.Parallel()

	api, client := started(t)

	if run, running, err := client.Current(t.Context(), rachiotest.DeviceID); err != nil || running {
		t.Fatalf("Current of a controller at rest = %+v, %t, %v", run, running, err)
	}

	api.Running = map[string]any{
		"deviceId": rachiotest.DeviceID, "scheduleId": rachiotest.FixedID, "type": "MANUAL", "status": "PROCESSING",
		"startDate": int64(1416467043933), "duration": 900, "zoneId": rachiotest.BackID,
		"zoneStartDate": int64(1416467343933), "zoneDuration": 600, "cycleCount": 1, "cycling": false,
	}

	run, running, err := client.Current(t.Context(), rachiotest.DeviceID)

	want := rachio.Run{
		ScheduleID: rachiotest.FixedID, Type: "MANUAL", Status: "PROCESSING",
		Started: time.UnixMilli(1416467043933), Duration: 15 * time.Minute,
		ZoneID: rachiotest.BackID, ZoneStarted: time.UnixMilli(1416467343933), ZoneDuration: 10 * time.Minute,
	}
	if err != nil || !running || run != want {
		t.Fatalf("Current = %+v, %t, %v\nwant %+v", run, running, err, want)
	}

	api.Status, api.Body = http.StatusOK, ""

	if run, running, err = client.Current(t.Context(), rachiotest.DeviceID); err != nil || running {
		t.Fatalf("Current answered nothing = %+v, %t, %v", run, running, err)
	}
}

// TestEvents: a range goes out in milliseconds, and the history comes
// back in Rachio's order; a range that ends before it starts is refused
// before anything is sent.
func TestEvents(t *testing.T) {
	t.Parallel()

	api, client := started(t)
	api.Events = []map[string]any{
		{
			"id": "79af85a5", "deviceId": rachiotest.DeviceID, "category": "SCHEDULE", "type": "SCHEDULE_STATUS",
			"eventDate": int64(1415677805635), "summary": "Every 3 Days at 5:00 AM completed",
		},
		{"id": "8bd1ac2f", "category": "SCHEDULE", "type": "ZONE_STATUS", "eventDate": int64(1415677205635)},
	}

	since, until := time.UnixMilli(1414818000000), time.UnixMilli(1415739608103)

	events, err := client.Events(t.Context(), rachiotest.DeviceID, since, until)

	want := []rachio.Event{
		{
			ID: "79af85a5", Category: "SCHEDULE", Type: "SCHEDULE_STATUS",
			At: time.UnixMilli(1415677805635), Summary: "Every 3 Days at 5:00 AM completed",
		},
		{ID: "8bd1ac2f", Category: "SCHEDULE", Type: "ZONE_STATUS", At: time.UnixMilli(1415677205635)},
	}
	if err != nil || !slices.Equal(events, want) {
		t.Fatalf("Events = %+v, %v\nwant %+v", events, err, want)
	}

	if query := []string{"endTime=1415739608103&startTime=1414818000000"}; !slices.Equal(api.Queries, query) {
		t.Errorf("the range went out as %v, want %v", api.Queries, query)
	}

	if _, err = client.Events(t.Context(), rachiotest.DeviceID, until, since); !errors.Is(err, rachio.ErrArgument) {
		t.Errorf("Events of a range that ends before it starts = %v, want ErrArgument", err)
	}

	if len(api.Queries) != 1 {
		t.Errorf("the refused range was sent: %v", api.Queries)
	}
}

// TestCommands: each command is one PUT, to its route, with the id of
// what it acts on and its duration in seconds; several zones go out in
// the order given, counted from one.
func TestCommands(t *testing.T) {
	t.Parallel()

	api, client := started(t)
	ctx := t.Context()
	api.Running = map[string]any{"status": "PROCESSING"}

	for _, step := range []struct {
		do   func() error
		want string
	}{
		{
			func() error { return client.StartZone(ctx, rachiotest.FrontID, 10*time.Minute) },
			`zone/start map[duration:600 id:` + rachiotest.FrontID + `]`,
		},
		{
			func() error { return client.StartZone(ctx, rachiotest.BackID, rachio.MaxZoneRun) },
			`zone/start map[duration:10800 id:` + rachiotest.BackID + `]`,
		},
		{
			func() error {
				return client.StartZones(ctx, []rachio.ZoneRun{
					{ZoneID: rachiotest.BackID, Duration: 90 * time.Second},
					{ZoneID: rachiotest.FrontID, Duration: time.Second},
				})
			},
			`zone/start_multiple map[zones:[map[duration:90 id:` + rachiotest.BackID + ` sortOrder:1] ` +
				`map[duration:1 id:` + rachiotest.FrontID + ` sortOrder:2]]]`,
		},
		{
			func() error { return client.StopWater(ctx, rachiotest.DeviceID) },
			`device/stop_water map[id:` + rachiotest.DeviceID + `]`,
		},
		{
			func() error { return client.RainDelay(ctx, rachiotest.DeviceID, 24*time.Hour) },
			`device/rain_delay map[duration:86400 id:` + rachiotest.DeviceID + `]`,
		},
		{
			func() error { return client.RainDelay(ctx, rachiotest.DeviceID, 0) },
			`device/rain_delay map[duration:0 id:` + rachiotest.DeviceID + `]`,
		},
		{
			func() error { return client.Standby(ctx, rachiotest.DeviceID, true) },
			`device/off map[id:` + rachiotest.DeviceID + `]`,
		},
		{
			func() error { return client.Standby(ctx, rachiotest.DeviceID, false) },
			`device/on map[id:` + rachiotest.DeviceID + `]`,
		},
		{
			func() error { return client.PauseRun(ctx, rachiotest.DeviceID, rachio.MaxPause) },
			`device/pause_zone_run map[duration:3600 id:` + rachiotest.DeviceID + `]`,
		},
		{
			func() error { return client.ResumeRun(ctx, rachiotest.DeviceID) },
			`device/resume_zone_run map[id:` + rachiotest.DeviceID + `]`,
		},
		{
			func() error { return client.StartSchedule(ctx, rachiotest.FixedID) },
			`schedulerule/start map[id:` + rachiotest.FixedID + `]`,
		},
		{
			func() error { return client.SkipSchedule(ctx, rachiotest.FlexID) },
			`schedulerule/skip map[id:` + rachiotest.FlexID + `]`,
		},
	} {
		before := len(api.Received())

		if err := step.do(); err != nil {
			t.Fatalf("the command for %s: %v", step.want, err)
		}

		sent := api.Received()[before:]
		if len(sent) != 1 || fmt.Sprint(sent[0].Route, " ", sent[0].Body) != step.want {
			t.Errorf("sent %v, want %s", sent, step.want)
		}
	}

	if api.Running != nil || !api.On || api.RainDelayUntil.IsZero() {
		t.Errorf("after the commands: running %v, on %t, delayed until %s", api.Running, api.On, api.RainDelayUntil)
	}
}

// TestArguments: an id that is empty and a duration outside the range the
// documentation gives are ErrArgument, with nothing sent.
func TestArguments(t *testing.T) {
	t.Parallel()

	api, client := started(t)
	ctx := t.Context()

	for name, do := range map[string]func() error{
		"a zone for no time":        func() error { return client.StartZone(ctx, rachiotest.FrontID, 0) },
		"a zone for under a second": func() error { return client.StartZone(ctx, rachiotest.FrontID, time.Millisecond) },
		"a zone for over three hours": func() error {
			return client.StartZone(ctx, rachiotest.FrontID, rachio.MaxZoneRun+time.Second)
		},
		"no zone":  func() error { return client.StartZone(ctx, "", time.Minute) },
		"no zones": func() error { return client.StartZones(ctx, nil) },
		"zones, one with no id": func() error {
			return client.StartZones(ctx, []rachio.ZoneRun{{ZoneID: rachiotest.FrontID, Duration: time.Minute}, {}})
		},
		"zones, one for too long": func() error {
			return client.StartZones(ctx, []rachio.ZoneRun{{ZoneID: rachiotest.FrontID, Duration: 4 * time.Hour}})
		},
		"a rain delay below zero": func() error { return client.RainDelay(ctx, rachiotest.DeviceID, -time.Second) },
		"a rain delay over a week": func() error {
			return client.RainDelay(ctx, rachiotest.DeviceID, rachio.MaxRainDelay+time.Second)
		},
		"a pause over an hour": func() error { return client.PauseRun(ctx, rachiotest.DeviceID, 2*time.Hour) },
		"a pause of no time":   func() error { return client.PauseRun(ctx, rachiotest.DeviceID, 0) },
		"no device to stop":    func() error { return client.StopWater(ctx, "") },
		"no schedule":          func() error { return client.StartSchedule(ctx, "") },
		"no device to read": func() error {
			_, err := client.Device(ctx, "")

			return err
		},
		"no device to ask what runs": func() error {
			_, _, err := client.Current(ctx, "")

			return err
		},
	} {
		if err := do(); !errors.Is(err, rachio.ErrArgument) {
			t.Errorf("%s = %v, want ErrArgument", name, err)
		}
	}

	if len(api.Received()) != 0 || len(api.Reads) != 0 {
		t.Errorf("the refused arguments sent %v and read %v", api.Received(), api.Reads)
	}

	if _, err := rachio.New("", rachio.Options{BaseURL: api.URL}).Person(ctx); !errors.Is(err, rachio.ErrArgument) {
		t.Errorf("Person with no key = %v, want ErrArgument", err)
	}
}

// TestLimit: the count the last answer's headers gave; the reset in the
// form the documentation shows and in the one it names; none before any
// answer.
func TestLimit(t *testing.T) {
	t.Parallel()

	api, client := started(t)

	if limit, seen := client.Limit(); seen {
		t.Fatalf("Limit before any request = %+v", limit)
	}

	for written, want := range map[string]time.Time{
		"2025-01-03T00:00:00Z":          time.Date(2025, time.January, 3, 0, 0, 0, 0, time.UTC),
		"Fri, 03 Jan 2025 00:00:00 GMT": time.Date(2025, time.January, 3, 0, 0, 0, 0, time.UTC),
		"tomorrow":                      {},
	} {
		api.Reset = written
		api.Remaining--

		if _, err := client.Device(t.Context(), rachiotest.DeviceID); err != nil {
			t.Fatalf("Device: %v", err)
		}

		limit, seen := client.Limit()
		if !seen || limit.Limit != 3500 || limit.Remaining != api.Remaining || !limit.Reset.Equal(want) {
			t.Errorf("Limit with a reset of %q = %+v, %t; want %d left until %s",
				written, limit, seen, api.Remaining, want)
		}
	}
}

// TestLimitKept: an answer without the limit's headers leaves the count
// the last one gave.
func TestLimitKept(t *testing.T) {
	t.Parallel()

	api, client := started(t)

	if _, err := client.Device(t.Context(), rachiotest.DeviceID); err != nil {
		t.Fatalf("Device: %v", err)
	}

	before, _ := client.Limit()
	api.Remaining = -1

	if _, err := client.Device(t.Context(), rachiotest.DeviceID); err != nil {
		t.Fatalf("Device: %v", err)
	}

	if after, seen := client.Limit(); !seen || after != before || before.Remaining != 3499 {
		t.Errorf("Limit after an answer with no headers = %+v, %t; it was %+v", after, seen, before)
	}
}

// TestAskedOnce: the account's id is asked of Rachio once, however many
// callers want the account at once.
func TestAskedOnce(t *testing.T) {
	t.Parallel()

	api, client := started(t)

	var callers sync.WaitGroup

	for range 20 {
		callers.Go(func() {
			if _, err := client.Person(t.Context()); err != nil {
				t.Errorf("Person: %v", err)
			}
		})
	}

	callers.Wait()

	asked := 0

	for _, read := range api.Reads {
		if read == "person/info" {
			asked++
		}
	}

	if asked != 1 || len(api.Reads) != 21 {
		t.Errorf("twenty callers asked whose key it is %d times, in %d reads", asked, len(api.Reads))
	}
}

// TestRefusals: each answer that is not a success is the error of its
// class, with Rachio's message and code when it gave them; an answer that
// is not the documented JSON, or too large, is ErrAnswer.
func TestRefusals(t *testing.T) {
	t.Parallel()

	api, client := started(t)
	ctx := t.Context()

	other := rachio.New("another-key", rachio.Options{BaseURL: api.URL, HTTP: api.Client()})
	if _, err := other.Person(ctx); !errors.Is(err, rachio.ErrCredentials) ||
		!strings.Contains(err.Error(), `HTTP 401: "The client is not authorized."`) {
		t.Errorf("Person under a key that is not the account's = %v, want ErrCredentials with Rachio's words", err)
	}

	// An id goes into a path escaped.
	if _, err := client.Device(ctx, "a/b?c"); !errors.Is(err, rachio.ErrNotFound) ||
		!slices.Contains(api.Reads, "device/a%2Fb%3Fc") {
		t.Errorf("Device of an id with a slash in it = %v, read as %v", err, api.Reads)
	}

	if err := client.StartZone(ctx, "no-such-zone", time.Minute); !errors.Is(err, rachio.ErrNotFound) {
		t.Errorf("StartZone of a zone that is none = %v, want ErrNotFound", err)
	}

	for _, answer := range []struct {
		status int
		body   string
		want   error
		says   string
	}{
		{http.StatusBadRequest, `{"error": "Missing data", "code": 301}`, rachio.ErrRefused, `code 301: "Missing data"`},
		{http.StatusBadRequest, `{}`, rachio.ErrRefused, "HTTP 400"},
		{http.StatusForbidden, ``, rachio.ErrCredentials, "HTTP 403"},
		{http.StatusTooManyRequests, `slow down`, rachio.ErrRateLimited, "HTTP 429"},
		{http.StatusInternalServerError, `<html>`, rachio.ErrRefused, "HTTP 500"},
		{http.StatusOK, `<html>`, rachio.ErrAnswer, "GET /device/"},
		{http.StatusOK, strings.Repeat(" ", 16<<20+1), rachio.ErrAnswer, "over 16777216 bytes"},
	} {
		api.Status, api.Body = answer.status, answer.body

		_, err := client.Device(ctx, rachiotest.DeviceID)
		if !errors.Is(err, answer.want) || !strings.Contains(err.Error(), answer.says) {
			t.Errorf("Device answered %d = %v, want %v saying %q", answer.status, err, answer.want, answer.says)
		}
	}

	// Any success is one: the documentation shows 204 for a command.
	for _, status := range []int{http.StatusCreated, http.StatusAccepted} {
		api.Status, api.Body = status, ``

		if err := client.StopWater(ctx, rachiotest.DeviceID); err != nil {
			t.Errorf("StopWater answered %d = %v", status, err)
		}
	}

	api.Status, api.Body = http.StatusOK, `{}`

	if _, err := client.Person(ctx); !errors.Is(err, rachio.ErrAnswer) {
		t.Errorf("Person when Rachio names no account = %v, want ErrAnswer", err)
	}
}

// holder keeps a Client where fmt cannot call its Format.
type holder struct {
	byValue   rachio.Client
	byPointer *rachio.Client
}

// TestTheKeyStaysIn: a Client formatted, by value, by pointer or inside
// another struct, under any verb, shows nothing of its key.
func TestTheKeyStaysIn(t *testing.T) {
	t.Parallel()

	const key = "f3a9-secret-key"

	client := rachio.New(key, rachio.Options{})

	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d"} {
		for name, value := range map[string]any{
			"a pointer": client, "a value": *client, "a holder": holder{*client, client}, "a holder's pointer": &holder{*client, client},
		} {
			if printed := fmt.Sprintf(verb, value); strings.Contains(printed, key) ||
				strings.Contains(printed, fmt.Sprintf("%x", key)) {
				t.Errorf("%s under %s shows the key: %s", name, verb, printed)
			}
		}
	}
}
