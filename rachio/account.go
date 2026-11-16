package rachio

import (
	"context"
	"fmt"
	"slices"
	"time"
)

// Person is the account: its owner and its controllers.
type Person struct {
	ID       string
	Username string
	FullName string
	Email    string
	Devices  []Device
}

// Device is one controller.
type Device struct {
	ID           string
	Name         string
	Model        string
	SerialNumber string
	// Status is the controller's connection as Rachio words it: the
	// documentation shows ONLINE and OFFLINE.
	Status string
	// On is false for a controller in standby: it runs no schedule.
	On bool
	// Paused is the documentation's field of that name; it says nothing
	// more of it.
	Paused bool
	// TimeZone is where the controller is, as an IANA name.
	TimeZone string
	// RainDelayUntil is when a rain delay ends; zero when there was never
	// one. One that is past is no delay.
	RainDelayUntil    time.Time
	RainSensorTripped bool
	// Zones is the controller's zones, by their number.
	Zones []Zone
	// Schedules is the controller's schedules, the fixed ones then the
	// flex ones.
	Schedules []ScheduleRef
}

// Online is whether Rachio hears the controller.
func (d *Device) Online() bool { return d.Status == "ONLINE" }

// Zone is one valve of a controller.
type Zone struct {
	ID      string
	Number  int
	Name    string
	Enabled bool
	// LastWatered is zero when the answer carries none.
	LastWatered time.Time
}

// ScheduleRef is a schedule as a controller lists it.
type ScheduleRef struct {
	ID string
	// Name is the schedule's name, or the one Rachio shows for it when it
	// was given none.
	Name string
	// Flex is true for a schedule Rachio plans from the weather and the
	// soil.
	Flex bool
}

// The account as the API writes it. No answer has a schema in the
// documentation; these are the fields its examples show.
type (
	wirePerson struct {
		ID       string       `json:"id"`
		Username string       `json:"username"`
		FullName string       `json:"fullName"`
		Email    string       `json:"email"`
		Devices  []wireDevice `json:"devices"`
	}
	wireDevice struct {
		ID                      string     `json:"id"`
		Name                    string     `json:"name"`
		Model                   string     `json:"model"`
		SerialNumber            string     `json:"serialNumber"`
		Status                  string     `json:"status"`
		On                      bool       `json:"on"`
		Paused                  bool       `json:"paused"`
		TimeZone                string     `json:"timeZone"`
		RainDelayExpirationDate int64      `json:"rainDelayExpirationDate"`
		RainSensorTripped       bool       `json:"rainSensorTripped"`
		Zones                   []wireZone `json:"zones"`
		ScheduleRules           []wireRef  `json:"scheduleRules"`
		FlexScheduleRules       []wireRef  `json:"flexScheduleRules"`
	}
	wireZone struct {
		ID              string `json:"id"`
		ZoneNumber      int    `json:"zoneNumber"`
		Name            string `json:"name"`
		Enabled         bool   `json:"enabled"`
		LastWateredDate int64  `json:"lastWateredDate"`
	}
	wireRef struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		ExternalName string `json:"externalName"`
	}
)

// Person reads the account with its controllers, their zones and their
// schedules. The first call asks Rachio whose key this is, once.
func (c *Client) Person(ctx context.Context) (Person, error) {
	person, err := c.PersonID(ctx)
	if err != nil {
		return Person{}, err
	}

	path, err := record("person", person, "")
	if err != nil {
		return Person{}, err
	}

	var wire wirePerson
	if err = c.read(ctx, path, nil, &wire); err != nil {
		return Person{}, err
	}

	account := Person{ID: wire.ID, Username: wire.Username, FullName: wire.FullName, Email: wire.Email}

	for _, device := range wire.Devices {
		account.Devices = append(account.Devices, device.public())
	}

	return account, nil
}

// PersonID is the id of the account the key belongs to. Rachio is asked
// once, however many callers ask; it is the smallest request there is.
func (c *Client) PersonID(ctx context.Context) (string, error) {
	held := c.held()

	held.asking.Lock()
	defer held.asking.Unlock()

	held.mu.Lock()
	person := held.person
	held.mu.Unlock()

	if person != "" {
		return person, nil
	}

	var info wirePerson
	if err := c.read(ctx, "/person/info", nil, &info); err != nil {
		return "", err
	}

	if info.ID == "" {
		return "", fmt.Errorf("%w: GET /person/info names no account", ErrAnswer)
	}

	held.mu.Lock()
	held.person = info.ID
	held.mu.Unlock()

	return info.ID, nil
}

// Device reads one controller.
func (c *Client) Device(ctx context.Context, id string) (Device, error) {
	path, err := record("device", id, "")
	if err != nil {
		return Device{}, err
	}

	var wire wireDevice
	if err = c.read(ctx, path, nil, &wire); err != nil {
		return Device{}, err
	}

	return wire.public(), nil
}

func (d *wireDevice) public() Device {
	device := Device{
		ID:                d.ID,
		Name:              d.Name,
		Model:             d.Model,
		SerialNumber:      d.SerialNumber,
		Status:            d.Status,
		On:                d.On,
		Paused:            d.Paused,
		TimeZone:          d.TimeZone,
		RainDelayUntil:    stamp(d.RainDelayExpirationDate),
		RainSensorTripped: d.RainSensorTripped,
	}

	for _, zone := range d.Zones {
		device.Zones = append(device.Zones, Zone{
			ID:          zone.ID,
			Number:      zone.ZoneNumber,
			Name:        zone.Name,
			Enabled:     zone.Enabled,
			LastWatered: stamp(zone.LastWateredDate),
		})
	}

	slices.SortStableFunc(device.Zones, func(a, b Zone) int { return a.Number - b.Number })

	for _, rule := range d.ScheduleRules {
		device.Schedules = append(device.Schedules, rule.public(false))
	}

	for _, rule := range d.FlexScheduleRules {
		device.Schedules = append(device.Schedules, rule.public(true))
	}

	return device
}

func (r wireRef) public(flex bool) ScheduleRef {
	name := r.Name
	if name == "" {
		name = r.ExternalName
	}

	return ScheduleRef{ID: r.ID, Name: name, Flex: flex}
}
