// Package cli is the rachio command: what Rachio's cloud API says of an
// account's controllers, their zones and schedules and what waters now,
// and the commands that water a zone, stop, delay for rain and put a
// controller in standby. The account's API key comes from the environment
// and from nowhere else.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/alecthomas/kong"

	"github.com/duncanite/go-rachio/rachio"
)

// Exit statuses.
const (
	ExitOK    = 0
	ExitError = 1
)

// EnvKey is the environment variable the API key is read from.
const EnvKey = "RACHIO_API_KEY" // #nosec G101 -- the name of an environment variable, not a credential

// clock is how the command prints a time, in the controller's zone.
const clock = "2006-01-02 15:04"

// errUsage is a command line, or an environment, the command cannot act
// on.
var errUsage = errors.New("cannot run")

// exitStatus is how the parser's own exit, on --help, leaves Run: kong
// calls its exit function and goes on, so the function panics with the
// status and Run recovers it.
type exitStatus int

type rootCommand struct {
	API    string `default:"https://api.rach.io/1/public" env:"RACHIO_API"                                                                           help:"Where Rachio's API is."`
	Device string `env:"RACHIO_DEVICE"                    help:"The controller, by its name or its id; the account's only controller when left out."`

	Devices   devicesCommand   `cmd:"" help:"List the account's controllers."`
	Zones     zonesCommand     `cmd:"" help:"List the controller's zones."`
	Schedules schedulesCommand `cmd:"" help:"List the controller's schedules."`
	Status    statusCommand    `cmd:"" help:"Say what the controller is doing: what waters now, a rain delay, standby."`
	Water     waterCommand     `cmd:"" help:"Water one zone for a given time, in place of what was watering."`
	Stop      stopCommand      `cmd:"" help:"Stop all watering."`
	RainDelay rainDelayCommand `cmd:"" help:"Keep the controller from watering for a given time."`
	Off       offCommand       `cmd:"" help:"Put the controller in standby: it runs no schedule."`
	On        onCommand        `cmd:"" help:"Take the controller out of standby."`
	Run       runCommand       `cmd:"" help:"Run a schedule now."`
	Skip      skipCommand      `cmd:"" help:"Skip a schedule."`
	Events    eventsCommand    `cmd:"" help:"Print the controller's history."`
	Limit     limitCommand     `cmd:"" help:"Say how many of the day's requests are left."`

	out    io.Writer
	client *rachio.Client
}

type (
	devicesCommand   struct{}
	zonesCommand     struct{}
	schedulesCommand struct{}
	statusCommand    struct{}
	stopCommand      struct{}
	offCommand       struct{}
	onCommand        struct{}
	limitCommand     struct{}
	waterCommand     struct {
		Zone string        `arg:"" help:"The zone, by its number, its name or its id."`
		For  time.Duration `arg:"" help:"How long, in hours, minutes and seconds: 10m, 90s, 1h30m. Three hours at most."`
	}
	rainDelayCommand struct {
		For time.Duration `arg:"" help:"How long, in hours, minutes and seconds: 24h, 36h. Seven days at most."`
	}
	runCommand struct {
		Schedule string `arg:"" help:"The schedule, by its name or its id."`
	}
	skipCommand struct {
		Schedule string `arg:"" help:"The schedule, by its name or its id."`
	}
	eventsCommand struct {
		Last time.Duration `default:"24h" help:"How far back from now, in hours, minutes and seconds: 6h, 72h."`
	}
)

// Run parses args and runs the command. client makes every request to
// Rachio; getenv reads the environment, where the API key is.
func Run(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
	client *http.Client,
	getenv func(string) string,
) (status int) {
	defer func() {
		if recovered := recover(); recovered != nil {
			exited, ok := recovered.(exitStatus)
			if !ok {
				panic(recovered)
			}

			status = int(exited)
		}
	}()

	root := rootCommand{out: stdout}

	parser, err := kong.New(&root,
		kong.Name("rachio"),
		kong.Description("Ask Rachio's cloud API about an account's irrigation controllers, and drive them. "+
			"The account's API key is read from "+EnvKey+"."),
		kong.UsageOnError(),
		kong.Writers(stdout, stderr),
		kong.Exit(func(code int) { panic(exitStatus(code)) }),
		kong.BindTo(ctx, (*context.Context)(nil)),
	)
	if err != nil {
		fmt.Fprintln(stderr, "rachio:", err)

		return ExitError
	}

	kctx, err := parser.Parse(args)
	if err != nil {
		parser.Errorf("%s", err)

		return ExitError
	}

	if key := getenv(EnvKey); key == "" {
		err = fmt.Errorf("%w: not set in the environment: %s", errUsage, EnvKey)
	} else {
		root.client = rachio.New(key, rachio.Options{BaseURL: root.API, HTTP: client})
		err = kctx.Run(&root)
	}

	if err != nil {
		fmt.Fprintln(stderr, "rachio:", err)

		return ExitError
	}

	return ExitOK
}

// controller is the controller a command acts on: the one --device names,
// by its name or its id, or the account's only one.
func (root *rootCommand) controller(ctx context.Context) (rachio.Device, error) {
	person, err := root.client.Person(ctx)
	if err != nil {
		return rachio.Device{}, err
	}

	var (
		found []rachio.Device
		names []string
	)

	for _, device := range person.Devices {
		names = append(names, strconv.Quote(device.Name))

		if root.Device == "" || device.ID == root.Device || strings.EqualFold(device.Name, root.Device) {
			found = append(found, device)
		}
	}

	switch {
	case len(found) == 1:
		return found[0], nil
	case root.Device != "":
		return rachio.Device{}, fmt.Errorf("%w: %d controllers are %q; the account has %s",
			errUsage, len(found), root.Device, strings.Join(names, ", "))
	default:
		return rachio.Device{}, fmt.Errorf("%w: the account has %d controllers, name one with --device: %s",
			errUsage, len(found), strings.Join(names, ", "))
	}
}

// zone is the controller's zone asked for: by its number, its id, or its
// name whatever its case.
func zone(device *rachio.Device, asked string) (rachio.Zone, error) {
	number, err := strconv.Atoi(asked)
	numbered := err == nil

	var (
		found []rachio.Zone
		names []string
	)

	for _, one := range device.Zones {
		names = append(names, fmt.Sprintf("%d %q", one.Number, one.Name))

		if numbered && one.Number == number || one.ID == asked || strings.EqualFold(one.Name, asked) {
			found = append(found, one)
		}
	}

	if len(found) != 1 {
		return rachio.Zone{}, fmt.Errorf("%w: %d zones are %q; the controller has %s",
			errUsage, len(found), asked, strings.Join(names, ", "))
	}

	return found[0], nil
}

// schedule is the controller's schedule asked for: by its id, or its name
// whatever its case.
func schedule(device *rachio.Device, asked string) (rachio.ScheduleRef, error) {
	var (
		found []rachio.ScheduleRef
		names []string
	)

	for _, one := range device.Schedules {
		names = append(names, strconv.Quote(one.Name))

		if one.ID == asked || strings.EqualFold(one.Name, asked) {
			found = append(found, one)
		}
	}

	if len(found) != 1 {
		return rachio.ScheduleRef{}, fmt.Errorf("%w: %d schedules are %q; the controller has %s",
			errUsage, len(found), asked, strings.Join(names, ", "))
	}

	return found[0], nil
}

// where is the zone a controller's times are printed in: its own, or this
// machine's when Rachio names one this machine does not know.
func where(device *rachio.Device) *time.Location {
	if location, err := time.LoadLocation(device.TimeZone); err == nil && device.TimeZone != "" {
		return location
	}

	//nolint:gosmopolitan // the fallback for a zone this machine cannot load
	return time.Local
}

func (*devicesCommand) Run(ctx context.Context, root *rootCommand) error {
	person, err := root.client.Person(ctx)
	if err != nil {
		return err
	}

	table := tabwriter.NewWriter(root.out, 0, 0, 2, ' ', 0)

	fmt.Fprintln(table, "NAME\tCONNECTED\tSTANDBY\tZONES\tSCHEDULES\tMODEL\tID")

	for _, device := range person.Devices {
		// The name is its owner's text.
		fmt.Fprintf(table, "%q\t%s\t%s\t%d\t%d\t%s\t%s\n",
			device.Name, yesNo(device.Online()), yesNo(!device.On), len(device.Zones), len(device.Schedules),
			plain(device.Model), plain(device.ID))
	}

	return flush(table)
}

func (*zonesCommand) Run(ctx context.Context, root *rootCommand) error {
	device, err := root.controller(ctx)
	if err != nil {
		return err
	}

	table := tabwriter.NewWriter(root.out, 0, 0, 2, ' ', 0)

	fmt.Fprintln(table, "ZONE\tNAME\tENABLED\tLAST WATERED\tID")

	for _, one := range device.Zones {
		watered := "-"
		if !one.LastWatered.IsZero() {
			watered = one.LastWatered.In(where(&device)).Format(clock)
		}

		fmt.Fprintf(table, "%d\t%q\t%s\t%s\t%s\n", one.Number, one.Name, yesNo(one.Enabled), watered, plain(one.ID))
	}

	return flush(table)
}

func (*schedulesCommand) Run(ctx context.Context, root *rootCommand) error {
	device, err := root.controller(ctx)
	if err != nil {
		return err
	}

	table := tabwriter.NewWriter(root.out, 0, 0, 2, ' ', 0)

	fmt.Fprintln(table, "NAME\tKIND\tID")

	for _, one := range device.Schedules {
		kind := "fixed"
		if one.Flex {
			kind = "flex"
		}

		fmt.Fprintf(table, "%q\t%s\t%s\n", one.Name, kind, plain(one.ID))
	}

	return flush(table)
}

func (*statusCommand) Run(ctx context.Context, root *rootCommand) error {
	device, err := root.controller(ctx)
	if err != nil {
		return err
	}

	run, running, err := root.client.Current(ctx, device.ID)
	if err != nil {
		return err
	}

	zone := where(&device)
	table := tabwriter.NewWriter(root.out, 0, 0, 1, ' ', 0)

	connected := "not connected"
	if device.Online() {
		connected = "connected"
	}

	fmt.Fprintf(table, "controller:\t%q, %s\n", device.Name, connected)
	fmt.Fprintf(table, "standby:\t%s\n", yesNo(!device.On))

	if device.RainDelayUntil.After(time.Now()) {
		fmt.Fprintf(table, "rain delay:\tuntil %s\n", device.RainDelayUntil.In(zone).Format(clock))
	}

	if device.RainSensorTripped {
		fmt.Fprintln(table, "rain sensor:\ttripped")
	}

	if running {
		fmt.Fprintf(table, "watering:\t%s\n", watering(&device, run, zone))
	} else {
		fmt.Fprintln(table, "watering:\tnothing")
	}

	return flush(table)
}

// watering is a run in a sentence: the zone, for how long and since when,
// with Rachio's own words for its kind and its state.
func watering(device *rachio.Device, run rachio.Run, zone *time.Location) string {
	name := strconv.Quote(run.ZoneID)

	for _, one := range device.Zones {
		if one.ID == run.ZoneID {
			name = fmt.Sprintf("zone %d %q", one.Number, one.Name)
		}
	}

	said := name + " for " + run.ZoneDuration.String()

	if !run.ZoneStarted.IsZero() {
		said += " since " + run.ZoneStarted.In(zone).Format(clock)
	}

	return fmt.Sprintf("%s (%q, %q)", said, run.Type, run.Status)
}

func (cmd *waterCommand) Run(ctx context.Context, root *rootCommand) error {
	device, err := root.controller(ctx)
	if err != nil {
		return err
	}

	asked, err := zone(&device, cmd.Zone)
	if err != nil {
		return err
	}

	return root.client.StartZone(ctx, asked.ID, cmd.For)
}

func (*stopCommand) Run(ctx context.Context, root *rootCommand) error {
	device, err := root.controller(ctx)
	if err != nil {
		return err
	}

	return root.client.StopWater(ctx, device.ID)
}

func (cmd *rainDelayCommand) Run(ctx context.Context, root *rootCommand) error {
	device, err := root.controller(ctx)
	if err != nil {
		return err
	}

	return root.client.RainDelay(ctx, device.ID, cmd.For)
}

func (*offCommand) Run(ctx context.Context, root *rootCommand) error {
	device, err := root.controller(ctx)
	if err != nil {
		return err
	}

	return root.client.Standby(ctx, device.ID, true)
}

func (*onCommand) Run(ctx context.Context, root *rootCommand) error {
	device, err := root.controller(ctx)
	if err != nil {
		return err
	}

	return root.client.Standby(ctx, device.ID, false)
}

func (cmd *runCommand) Run(ctx context.Context, root *rootCommand) error {
	device, err := root.controller(ctx)
	if err != nil {
		return err
	}

	asked, err := schedule(&device, cmd.Schedule)
	if err != nil {
		return err
	}

	return root.client.StartSchedule(ctx, asked.ID)
}

func (cmd *skipCommand) Run(ctx context.Context, root *rootCommand) error {
	device, err := root.controller(ctx)
	if err != nil {
		return err
	}

	asked, err := schedule(&device, cmd.Schedule)
	if err != nil {
		return err
	}

	return root.client.SkipSchedule(ctx, asked.ID)
}

func (cmd *eventsCommand) Run(ctx context.Context, root *rootCommand) error {
	if cmd.Last <= 0 {
		return fmt.Errorf("%w: --last wants a time above zero", errUsage)
	}

	device, err := root.controller(ctx)
	if err != nil {
		return err
	}

	now := time.Now()

	events, err := root.client.Events(ctx, device.ID, now.Add(-cmd.Last), now)
	if err != nil {
		return err
	}

	if len(events) == 0 {
		fmt.Fprintln(root.out, "no event")

		return nil
	}

	table := tabwriter.NewWriter(root.out, 0, 0, 2, ' ', 0)

	fmt.Fprintln(table, "AT\tTYPE\tSUMMARY")

	for _, event := range events {
		// The summary carries the names the account gave its schedules.
		fmt.Fprintf(table, "%s\t%s\t%q\n", event.At.In(where(&device)).Format(clock), plain(event.Type), event.Summary)
	}

	return flush(table)
}

func (*limitCommand) Run(ctx context.Context, root *rootCommand) error {
	// The count comes with any answer; the account's id is the smallest.
	if _, err := root.client.PersonID(ctx); err != nil {
		return err
	}

	limit, seen := root.client.Limit()
	if !seen {
		fmt.Fprintln(root.out, "Rachio's answer carried no count")

		return nil
	}

	said := fmt.Sprintf("%d of %d requests left", limit.Remaining, limit.Limit)
	if !limit.Reset.IsZero() {
		said += " until " + limit.Reset.UTC().Format(clock) + " UTC"
	}

	fmt.Fprintln(root.out, said)

	return nil
}

func yesNo(yes bool) string {
	if yes {
		return "yes"
	}

	return "no"
}

// plain is a word of Rachio's, an id, a model, a type, as a table prints
// it: as it is when it is letters, digits, dashes and underscores, quoted
// when it is anything else, and a dash when it is empty.
func plain(value string) string {
	if value == "" {
		return "-"
	}

	for _, char := range value {
		if char != '-' && char != '_' && !unicode.IsLetter(char) && !unicode.IsDigit(char) {
			return strconv.Quote(value)
		}
	}

	return value
}

func flush(table *tabwriter.Writer) error {
	if err := table.Flush(); err != nil {
		return fmt.Errorf("writing the table: %w", err)
	}

	return nil
}
