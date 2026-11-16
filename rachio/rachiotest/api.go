// Package rachiotest is Rachio's public API for tests: an HTTP server that
// answers the routes this module calls with the fields the documentation's
// examples show, over one account with one controller that a test reads
// and sets. It records every command it was sent.
//
// A key that is not the account's is answered as Rachio's servers answer
// one: 401, with a list of messages. The documentation leaves these out,
// and the fake chooses: an id that is no record is answered 404, and a
// controller that waters nothing answers its current schedule with an
// empty object.
package rachiotest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The account the fake holds.
const (
	Key      = "0a1b2c3d-key" // #nosec G101 -- the fake's own key, of no account
	PersonID = "c8d10892-fd69-48b3-8743-f111e4392d8a"
	DeviceID = "2a5e7d3c-c140-4e2e-91a1-a212a518adc5"
	FrontID  = "e02de192-5a2b-4669-95c6-34deea3d23cb"
	BackID   = "710b5add-4ce1-4d10-9a73-523823c7a82b"
	UnusedID = "68539eea-2308-433c-af5e-b3d92ae9c637"
	FixedID  = "cc9c6e6f-c285-4a7b-9911-ff6065e7ff5b"
	FlexID   = "8bb7ddb3-1881-40ac-b138-8b5a5b4073f0"
	TwinID   = "79af85a5-f040-4f59-9f7e-a6f06a7bb251"
)

// LastWatered is when the back lawn was last watered, in milliseconds
// since 1970.
const LastWatered int64 = 1416467043933

const (
	maxBody = 1 << 20
	// The day's limit, and what is left of it when the fake starts.
	limit     = 3500
	remaining = 3499
	// codeMissing is the code of the one error the documentation shows.
	codeMissing = 301
)

// Call is one command the fake was sent: its route, and its body as
// decoded JSON.
type Call struct {
	Route string
	Body  map[string]any
}

// API is the fake. A test sets its exported fields between requests; the
// server holds its lock around them.
type API struct {
	*httptest.Server

	mu sync.Mutex
	// Online, On and RainDelayUntil are the controller's.
	Online         bool
	On             bool
	RainDelayUntil time.Time
	// Running is the answer to the current schedule, the documentation's
	// fields; nil when nothing waters.
	Running map[string]any
	// Events is the answer to the events route.
	Events []map[string]any
	// Twin gives the account a second controller, named as the first but
	// for its case, with no zone and no schedule.
	Twin bool
	// Names is the names of records to answer in place of their own, by
	// id.
	Names map[string]string
	// Remaining is what the limit's header says is left, the headers left
	// out when it is below zero; Reset is that header's value, in
	// whichever form.
	Remaining int
	Reset     string
	// Status, when not zero, is the status every request is answered with,
	// and Body its body.
	Status int
	Body   string
	// Calls is every command received, in order; Reads every other route
	// asked; Queries every events query.
	Calls   []Call
	Reads   []string
	Queries []string
}

// New starts the fake: a controller online and on, watering nothing, with
// 3,499 requests left. The caller closes it.
func New() *API {
	api := &API{Online: true, On: true, Remaining: remaining, Reset: "2025-01-03T00:00:00Z"}
	api.Server = httptest.NewServer(http.HandlerFunc(api.serve))

	return api
}

// Received is the commands sent so far.
func (a *API) Received() []Call {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]Call(nil), a.Calls...)
}

func (a *API) serve(writer http.ResponseWriter, req *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()

	writer.Header().Set("Content-Type", "application/json")

	if a.Remaining >= 0 {
		writer.Header().Set("X-Ratelimit-Limit", strconv.Itoa(limit))
		writer.Header().Set("X-Ratelimit-Remaining", strconv.Itoa(a.Remaining))
		writer.Header().Set("X-Ratelimit-Reset", a.Reset)
	}

	status, answer := a.answer(req)

	if a.Status != 0 {
		writer.WriteHeader(a.Status)
		_, _ = io.WriteString(writer, a.Body)

		return
	}

	writer.WriteHeader(status)

	if answer != nil {
		_ = json.NewEncoder(writer).Encode(answer)
	}
}

// missing is the one error body the documentation shows.
func missing() (int, any) {
	return http.StatusBadRequest, map[string]any{"error": "Missing data", "code": codeMissing}
}

func (a *API) answer(req *http.Request) (int, any) {
	if req.Header.Get("Authorization") != "Bearer "+Key {
		// The body Rachio's servers send with their 401.
		return http.StatusUnauthorized, map[string]any{
			"errors": []any{map[string]string{"message": "The client is not authorized."}},
		}
	}

	name := strings.TrimPrefix(req.URL.Path, "/")

	if req.Method == http.MethodPut {
		return a.command(name, req)
	}

	// As it was sent, so that a test sees an id's escaping.
	a.Reads = append(a.Reads, strings.TrimPrefix(req.URL.EscapedPath(), "/"))

	switch name {
	case "person/info":
		return http.StatusOK, map[string]any{"id": PersonID}
	case "person/" + PersonID:
		return http.StatusOK, person{
			ID: PersonID, Username: "owner", FullName: "An Owner", Email: "owner@example.com",
			Devices: a.devices(),
		}
	case "device/" + DeviceID:
		return http.StatusOK, a.device()
	case "device/" + DeviceID + "/current_schedule":
		if a.Running == nil {
			return http.StatusOK, map[string]any{}
		}

		return http.StatusOK, a.Running
	case "device/" + DeviceID + "/event":
		a.Queries = append(a.Queries, req.URL.RawQuery)

		events := a.Events
		if events == nil {
			events = []map[string]any{}
		}

		return http.StatusOK, events
	}

	return http.StatusNotFound, nil
}

// route is one command: the records it acts on, whether it takes a
// duration, and what it changes in the fake.
type route struct {
	records []string
	timed   bool
	apply   func(api *API, length time.Duration)
}

func routes() map[string]route {
	controller := []string{DeviceID, TwinID}
	schedules := []string{FixedID, FlexID}

	return map[string]route{
		"device/stop_water": {records: controller, apply: func(api *API, _ time.Duration) { api.Running = nil }},
		"device/rain_delay": {records: controller, timed: true, apply: func(api *API, length time.Duration) {
			api.RainDelayUntil = time.Now().Add(length)
		}},
		"device/on":              {records: controller, apply: func(api *API, _ time.Duration) { api.On = true }},
		"device/off":             {records: controller, apply: func(api *API, _ time.Duration) { api.On = false }},
		"device/pause_zone_run":  {records: controller, timed: true},
		"device/resume_zone_run": {records: controller},
		"zone/start":             {records: []string{FrontID, BackID, UnusedID}, timed: true},
		"schedulerule/start":     {records: schedules},
		"schedulerule/skip":      {records: schedules},
	}
}

// command is the routes that change something: each takes the id of a
// record of the right kind, some a duration, and answers 204.
func (a *API) command(name string, req *http.Request) (int, any) {
	raw, _ := io.ReadAll(io.LimitReader(req.Body, maxBody))

	var body map[string]any
	if json.Unmarshal(raw, &body) != nil {
		return missing()
	}

	a.Calls = append(a.Calls, Call{Route: name, Body: body})

	if name == "zone/start_multiple" {
		if zones, ok := body["zones"].([]any); !ok || len(zones) == 0 {
			return missing()
		}

		return http.StatusNoContent, nil
	}

	asked, exists := routes()[name]
	if !exists {
		return http.StatusNotFound, nil
	}

	subject, named := body["id"].(string)
	length, timed := body["duration"].(float64)

	switch {
	case !named || subject == "", asked.timed && !timed:
		return missing()
	case !slices.Contains(asked.records, subject):
		return http.StatusNotFound, nil
	}

	if asked.apply != nil {
		asked.apply(a, time.Duration(length)*time.Second)
	}

	return http.StatusNoContent, nil
}

// The account as the documentation's examples write it.
type (
	person struct {
		ID       string   `json:"id"`
		Username string   `json:"username"`
		FullName string   `json:"fullName"`
		Email    string   `json:"email"`
		Devices  []device `json:"devices"`
	}
	device struct {
		ID                      string `json:"id"`
		Status                  string `json:"status"`
		Zones                   []zone `json:"zones"`
		TimeZone                string `json:"timeZone"`
		Name                    string `json:"name"`
		Model                   string `json:"model"`
		SerialNumber            string `json:"serialNumber"`
		ScheduleRules           []rule `json:"scheduleRules"`
		FlexScheduleRules       []rule `json:"flexScheduleRules"`
		RainDelayExpirationDate int64  `json:"rainDelayExpirationDate"`
		RainSensorTripped       bool   `json:"rainSensorTripped"`
		Paused                  bool   `json:"paused"`
		On                      bool   `json:"on"`
	}
	zone struct {
		ID              string `json:"id"`
		ZoneNumber      int    `json:"zoneNumber"`
		Name            string `json:"name"`
		Enabled         bool   `json:"enabled"`
		LastWateredDate int64  `json:"lastWateredDate,omitempty"`
	}
	rule struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		ExternalName string `json:"externalName"`
	}
)

// devices is the account's controllers: the one, and its twin when there
// is one.
func (a *API) devices() []device {
	if !a.Twin {
		return []device{a.device()}
	}

	return []device{a.device(), {ID: TwinID, Status: "ONLINE", Name: "GARDEN", TimeZone: "America/Denver", On: true}}
}

// device is the controller: three zones, out of order, as nothing in the
// documentation orders them, a fixed schedule with no name of its own and
// a flex one.
func (a *API) device() device {
	controller := device{
		ID:     DeviceID,
		Status: "OFFLINE",
		Zones: []zone{
			{ID: BackID, ZoneNumber: 2, Name: "Back Lawn", Enabled: true, LastWateredDate: LastWatered},
			{ID: FrontID, ZoneNumber: 1, Name: "Front Beds", Enabled: true},
			{ID: UnusedID, ZoneNumber: 3, Name: "Zone 3"},
		},
		TimeZone:          "America/Denver",
		Name:              "Garden",
		Model:             "GENERATION2_8ZONE",
		SerialNumber:      "SERIAL0001",
		ScheduleRules:     []rule{{ID: FixedID, ExternalName: "Every Wed at 9:05 AM"}},
		FlexScheduleRules: []rule{{ID: FlexID, Name: "Summer lawn", ExternalName: "Summer lawn"}},
		On:                a.On,
	}

	if a.Online {
		controller.Status = "ONLINE"
	}

	for index := range controller.Zones {
		if name, renamed := a.Names[controller.Zones[index].ID]; renamed {
			controller.Zones[index].Name = name
		}
	}

	for index := range controller.FlexScheduleRules {
		if name, renamed := a.Names[controller.FlexScheduleRules[index].ID]; renamed {
			controller.FlexScheduleRules[index].Name = name
		}
	}

	if !a.RainDelayUntil.IsZero() {
		controller.RainDelayExpirationDate = a.RainDelayUntil.UnixMilli()
	}

	return controller
}
