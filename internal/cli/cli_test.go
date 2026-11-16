package cli_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/duncanite/go-rachio/internal/cli"
	"github.com/duncanite/go-rachio/rachio/rachiotest"
)

func started(t *testing.T) *rachiotest.API {
	t.Helper()

	api := rachiotest.New()
	t.Cleanup(api.Close)

	return api
}

// run is the command against the fake under the account's key, or under
// key when it is not empty; its output and what it wrote of errors, then
// its status.
func run(t *testing.T, api *rachiotest.API, key string, args ...string) (out, errs string, status int) {
	t.Helper()

	var stdout, stderr bytes.Buffer

	env := map[string]string{cli.EnvKey: key}

	args = append([]string{"--api", api.URL}, args...)
	status = cli.Run(t.Context(), args, &stdout, &stderr, api.Client(), func(name string) string { return env[name] })

	return stdout.String(), stderr.String(), status
}

// rows is the output as lines of fields, the spacing of a table aside.
func rows(out string) string {
	var lines []string

	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		lines = append(lines, strings.Join(strings.Fields(line), " "))
	}

	return strings.Join(lines, "\n")
}

// TestReading: each command that reads, its output as a person reads it,
// times in the controller's own zone.
func TestReading(t *testing.T) {
	t.Parallel()

	api := started(t)
	api.RainDelayUntil = time.Date(2099, time.May, 1, 18, 0, 0, 0, time.UTC)
	api.Running = map[string]any{
		"type": "MANUAL", "status": "PROCESSING", "zoneId": rachiotest.BackID,
		"zoneStartDate": int64(1416467043933), "zoneDuration": 600,
	}
	api.Events = []map[string]any{{
		"type": "ZONE_STATUS", "eventDate": int64(1416467043933), "summary": "Back Lawn began watering",
	}}

	for _, step := range []struct {
		args []string
		want string
	}{
		{[]string{"devices"}, "NAME CONNECTED STANDBY ZONES SCHEDULES MODEL ID\n" +
			`"Garden" yes no 3 2 GENERATION2_8ZONE ` + rachiotest.DeviceID},
		{[]string{"zones"}, strings.Join([]string{
			"ZONE NAME ENABLED LAST WATERED ID",
			`1 "Front Beds" yes - ` + rachiotest.FrontID,
			`2 "Back Lawn" yes 2014-11-20 00:04 ` + rachiotest.BackID,
			`3 "Zone 3" no - ` + rachiotest.UnusedID,
		}, "\n")},
		{[]string{"schedules"}, strings.Join([]string{
			"NAME KIND ID",
			`"Every Wed at 9:05 AM" fixed ` + rachiotest.FixedID,
			`"Summer lawn" flex ` + rachiotest.FlexID,
		}, "\n")},
		{[]string{"status"}, strings.Join([]string{
			`controller: "Garden", connected`,
			"standby: no",
			"rain delay: until 2099-05-01 12:00",
			`watering: zone 2 "Back Lawn" for 10m0s since 2014-11-20 00:04 ("MANUAL", "PROCESSING")`,
		}, "\n")},
		{[]string{"--device", "garden", "events", "--last", "6h"}, "AT TYPE SUMMARY\n" +
			`2014-11-20 00:04 ZONE_STATUS "Back Lawn began watering"`},
		{[]string{"limit"}, "3499 of 3500 requests left until 2025-01-03 00:00 UTC"},
	} {
		out, errs, status := run(t, api, rachiotest.Key, step.args...)
		if status != cli.ExitOK || errs != "" || rows(out) != step.want {
			t.Errorf("%v exits %d, saying %q and\n%s\nwant\n%s", step.args, status, errs, rows(out), step.want)
		}
	}

	api.Running, api.Events, api.Online, api.On = nil, nil, false, false
	api.RainDelayUntil = time.UnixMilli(1420027691501)

	out, _, _ := run(t, api, rachiotest.Key, "--device", rachiotest.DeviceID, "status")
	if want := "controller: \"Garden\", not connected\nstandby: yes\nwatering: nothing"; rows(out) != want {
		t.Errorf("status of a controller at rest, in standby, its delay past:\n%s\nwant\n%s", rows(out), want)
	}

	if out, _, _ = run(t, api, rachiotest.Key, "events"); rows(out) != "no event" {
		t.Errorf("events with none = %q", out)
	}
}

// TestActing: each command that changes something sends its one command,
// to the record its argument names, and prints nothing.
func TestActing(t *testing.T) {
	t.Parallel()

	api := started(t)

	for _, step := range []struct {
		args []string
		want string
	}{
		{[]string{"water", "2", "10m"}, `zone/start map[duration:600 id:` + rachiotest.BackID + `]`},
		{[]string{"water", "front beds", "90s"}, `zone/start map[duration:90 id:` + rachiotest.FrontID + `]`},
		{[]string{"water", rachiotest.UnusedID, "1h"}, `zone/start map[duration:3600 id:` + rachiotest.UnusedID + `]`},
		{[]string{"stop"}, `device/stop_water map[id:` + rachiotest.DeviceID + `]`},
		{[]string{"rain-delay", "36h"}, `device/rain_delay map[duration:129600 id:` + rachiotest.DeviceID + `]`},
		{[]string{"off"}, `device/off map[id:` + rachiotest.DeviceID + `]`},
		{[]string{"on"}, `device/on map[id:` + rachiotest.DeviceID + `]`},
		{[]string{"run", "summer LAWN"}, `schedulerule/start map[id:` + rachiotest.FlexID + `]`},
		{[]string{"skip", rachiotest.FixedID}, `schedulerule/skip map[id:` + rachiotest.FixedID + `]`},
	} {
		before := len(api.Received())

		out, errs, status := run(t, api, rachiotest.Key, step.args...)
		if status != cli.ExitOK || out != "" || errs != "" {
			t.Fatalf("%v exits %d, saying %q and %q", step.args, status, out, errs)
		}

		sent := api.Received()[before:]
		if len(sent) != 1 || fmt.Sprint(sent[0].Route, " ", sent[0].Body) != step.want {
			t.Errorf("%v sent %v, want %s", step.args, sent, step.want)
		}
	}
}

// TestAmbiguity: a name that two records answer to is refused, with the
// names there are and nothing sent; an id still names one.
func TestAmbiguity(t *testing.T) {
	t.Parallel()

	api := started(t)
	api.Twin = true
	api.Names = map[string]string{rachiotest.UnusedID: "front BEDS", rachiotest.FlexID: "2"}

	for _, step := range []struct {
		args []string
		want string
	}{
		{[]string{"stop"}, `the account has 2 controllers, name one with --device: "Garden", "GARDEN"`},
		{[]string{"--device", "garden", "stop"}, `2 controllers are "garden"; the account has "Garden", "GARDEN"`},
		{[]string{"--device", rachiotest.DeviceID, "water", "Front Beds", "5m"}, `2 zones are "Front Beds"`},
		{[]string{"--device", rachiotest.TwinID, "water", "1", "5m"}, `0 zones are "1"; the controller has `},
	} {
		out, errs, status := run(t, api, rachiotest.Key, step.args...)
		if status != cli.ExitError || out != "" || !strings.Contains(errs, step.want) {
			t.Errorf("%v exits %d, saying %q and %q, want an error with %q", step.args, status, out, errs, step.want)
		}
	}

	if sent := api.Received(); len(sent) != 0 {
		t.Fatalf("the ambiguous names sent %v", sent)
	}

	// A zone named as another's number: the number is two zones' then.
	api.Names[rachiotest.UnusedID] = "2"

	if _, errs, status := run(
		t,
		api,
		rachiotest.Key,
		"--device",
		rachiotest.DeviceID,
		"water",
		"2",
		"5m",
	); status != cli.ExitError ||
		!strings.Contains(errs, `2 zones are "2"`) {
		t.Errorf("water of a number that is also a name exits %d saying %q", status, errs)
	}

	for _, step := range []struct {
		args []string
		want string
	}{
		{[]string{"--device", rachiotest.DeviceID, "water", rachiotest.UnusedID, "5m"}, `zone/start map[duration:300 id:` + rachiotest.UnusedID + `]`},
		{[]string{"--device", rachiotest.DeviceID, "run", "2"}, `schedulerule/start map[id:` + rachiotest.FlexID + `]`},
		{[]string{"--device", rachiotest.TwinID, "stop"}, `device/stop_water map[id:` + rachiotest.TwinID + `]`},
	} {
		before := len(api.Received())

		if out, errs, status := run(
			t,
			api,
			rachiotest.Key,
			step.args...,
		); status != cli.ExitOK {
			t.Errorf("%v exits %d, saying %q and %q", step.args, status, out, errs)
		}

		sent := api.Received()[before:]
		if len(sent) != 1 || fmt.Sprint(sent[0].Route, " ", sent[0].Body) != step.want {
			t.Errorf("%v sent %v, want %s", step.args, sent, step.want)
		}
	}
}

// TestRefusing: what the command cannot do, it says on the error stream
// and exits 1, with no command sent: no key, a key Rachio refuses, a
// zone, a schedule or a controller that is none, with those there are, a
// time no zone runs for. --help exits 0 and names the key's variable.
func TestRefusing(t *testing.T) {
	t.Parallel()

	api := started(t)

	for _, step := range []struct {
		key  string
		args []string
		want string
	}{
		{"", []string{"devices"}, "not set in the environment: " + cli.EnvKey},
		{"another-key", []string{"devices"}, "rachio refused the API key"},
		{rachiotest.Key, []string{"water", "7", "10m"}, `0 zones are "7"; the controller has 1 "Front Beds", 2 "Back Lawn"`},
		{rachiotest.Key, []string{"water", "2", "4h"}, "a zone run of 4h0m0s, outside 1s to 3h0m0s"},
		{rachiotest.Key, []string{"water", "2", "soon"}, "soon"},
		{rachiotest.Key, []string{"water", "2"}, `expected "<for>"`},
		{rachiotest.Key, []string{"rain-delay", "200h"}, "a rain delay of 200h0m0s"},
		{rachiotest.Key, []string{"run", "Winter"}, `0 schedules are "Winter"; the controller has "Every Wed at 9:05 AM", "Summer lawn"`},
		{rachiotest.Key, []string{"--device", "Shed", "stop"}, `0 controllers are "Shed"; the account has "Garden"`},
		{rachiotest.Key, []string{"events", "--last", "0s"}, "--last wants a time above zero"},
	} {
		out, errs, status := run(t, api, step.key, step.args...)
		if status != cli.ExitError || out != "" || !strings.Contains(errs, step.want) {
			t.Errorf("%v exits %d, saying %q and %q, want an error with %q", step.args, status, out, errs, step.want)
		}

		if strings.Contains(errs, "another-key") || strings.Contains(errs, rachiotest.Key) {
			t.Errorf("%v shows the key: %q", step.args, errs)
		}
	}

	if sent := api.Received(); len(sent) != 0 {
		t.Errorf("the refused commands sent %v", sent)
	}

	out, _, status := run(t, api, "", "--help")
	if status != cli.ExitOK || !strings.Contains(out, cli.EnvKey) {
		t.Errorf("--help exits %d saying %q", status, out)
	}
}
