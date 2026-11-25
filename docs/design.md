# go-rachio: the design

Research into Rachio irrigation controllers, done on 2026-11-15 from public sources, and
the module built from it. The module follows Rachio's documentation. What it reads was
run against one account's controller on 2026-11-25; what it commands was not.

In short: Rachio is a cloud product with a good public API. The controller has no
interface on the LAN, and Rachio has said it plans none for this hardware. It does keep
its schedules and runs them when the network is down.

## The device

- The controller reaches Rachio's cloud over Wi-Fi. What it speaks to it is not
  documented.
- It stores its schedules: fixed ones keep running offline, weather skips need the cloud
  ([what works offline](https://support.rachio.com/en_us/offline-device-functionality-SyboPIkFv)).
- The Smart Hose Timer is another product on another API: a battery valve on Bluetooth
  to a Wi-Fi hub.

## Ways in

### Rachio's cloud API (official)

From [its documentation](https://rachio.readme.io/reference/getting-started):

- `https://api.rach.io/1` for controllers: the account, a device, its zones and its
  schedules. `https://cloud-rest.rach.io` for the hose timer.
- An API key from the Rachio app, sent as a bearer token. It is the whole account's.
- Read: the device and its zones, the current schedule, what is to run. Do: start a
  zone or several for a given time, stop all watering, set a rain delay, put the
  controller in standby, start or skip a schedule.
- Webhooks for events, signed; they want an address Rachio's servers can reach.
- [The limit](https://rachio.readme.io/reference/rate-limiting): "a maximum of 3,500
  requests per day across all of our provided API endpoints", reset at midnight UTC,
  with the count left in `X-RateLimit-Remaining`. Home Assistant's page gives a lower
  figure, so the headers are what to read.
- Rachio gives no support for the API beyond its forum.

### Local

None. Rachio's staff, on [its forum](https://community.rachio.com/t/api-call-to-local-sprinkler-ip/4152),
over several years: local access is not possible, and there are no plans for it on the
current hardware. No reverse-engineered protocol or replacement firmware was found.

### HomeKit, Matter

The Rachio 3 was given HomeKit in 2018; reports say Rachio suspended it in 2022. No
Rachio irrigation controller with Matter was found.

## What this module is

**A client of the cloud API**, the one way in.

- `rachio`: `New` takes the account's API key. `Person` reads the account with its
  controllers, their zones and schedules; `Device` one controller; `Current` what waters
  now; `Events` the history. The commands: `StartZone`, `StartZones`, `StopWater`,
  `RainDelay`, `Standby`, `PauseRun`, `ResumeRun`, `StartSchedule`, `SkipSchedule`.
  `Limit` is the day's count as the last answer's headers gave it. The standard library
  alone.
- `rachio/rachiotest`: the API for tests, one account with one controller.
- `cmd/rachio`: the command, over `internal/cli`.

### Decisions

- **The key is held where nothing prints it.** A client formatted, by value, by pointer
  or inside another struct, shows nothing of it. The command reads it from
  `RACHIO_API_KEY` and takes it as no flag.
- **A zone is started for a stated time**, three hours at most, a pause is an hour at
  most and a rain delay seven days: the documentation's limits, checked before anything
  is sent. Its ranges start at zero; a zone run and a pause of no time are refused here.
- **The answers are read by the fields the documentation's examples show.** No answer
  has a schema there; a field that is absent reads as its zero.
- **Events are asked for, not pushed.** A webhook wants an address Rachio's servers can
  reach.
- **Not built**: the forecast, a schedule's details and its seasonal adjustment, the
  moisture routes, webhooks, and the hose timer, which is another API.

### Where the documentation contradicts itself, or says nothing

- The reset of the daily count: the prose says RFC 1123, the example is
  `2025-01-03T00:00:00Z`. Both are read.
- Starting several zones: the schema names the body's key `zoneRunDurations`, the sample
  request sends `zones`. `zones` is sent, which is also what
  [rachiopy](https://github.com/rfverbruggen/rachiopy), the Python client behind Home
  Assistant's integration, sends.
- The events' range and the pause's fields are declared as path parameters and sent, in
  the samples, as a query string and a JSON body. The samples are followed.
- No status is given for a key refused, a record that is none, or the limit reached. 401
  and 403 are read as the key refused, 404 as no such record, 429 as the limit. Any 2xx
  is a success, where the pages show 200 and 204.
- What the current schedule answers when nothing waters is not said. An answer with no
  status in it, an empty one among them, is read as nothing.
- Whether a rain delay of zero ends a delay, and whether skipping a schedule skips the
  run under way or the next, are not said.

### Observed on Rachio's servers

One command and two requests, on 2026-11-15, under a key that is no account's:

- A refused key is answered 401, with the body `{"errors":[{"message":"The client is not
  authorized."}]}`: not the error the pages show under a 400. The client reads both.
- The 401 carries the limit's headers, and the reset is written as the example writes
  it, `2026-11-17T00:00:00Z`, not as the prose says.
- The count went down under the refused key.

Six commands, on 2026-11-25, under an account's key: `devices`, `zones`, `schedules`,
`status`, `events --last 72h` and `limit`. The account has one controller, a Rachio 3
with eight zones (the model reads `GENERATION3_8ZONE_USI`), and two schedules, one fixed
and one flex.

- Each of the six was answered and read: the controller with its counts of zones and
  schedules, the zones with their numbers, whether each is enabled and when it last
  watered, the schedules with their kinds.
- A zone that never watered has no time for it, and reads as none.
- With nothing watering, the current schedule's answer reads as nothing. The answer
  itself was not looked at.
- A range of events with none in it is answered, and reads as no event.
- The limit under a real key is the documentation's: 3,500 a day, the reset written as
  midnight UTC of the next day.

## Open questions

- Whether anything else already uses the same API key, and so its daily count.

## Not verified

- No command that acts was sent to a controller: a zone or several started, watering
  stopped, a rain delay, standby, a run paused or resumed, a schedule started or
  skipped. Those were run against the module's own stand-in only.
- An event, as Rachio's servers write one: the range asked had none.
- An account with several controllers, a controller of another generation, one that is
  offline, and what `status` reads while a zone waters.
- Whether what was read is right: it was not compared with Rachio's app.
- The choices listed under "Where the documentation contradicts itself, or says
  nothing" that a command or an event would show: the key several zones are started
  under, the events' range and the pause's fields, the statuses, a rain delay of zero,
  what skipping a schedule skips.
- What the controller speaks to Rachio's cloud, and its chips.
- HomeKit's suspension is from press, not from Rachio.
- Any terms of use for the API: none found on its pages.

## Sources

- Rachio's API: [getting started](https://rachio.readme.io/reference/getting-started),
  [authentication](https://rachio.readme.io/reference/authentication),
  [rate limiting](https://rachio.readme.io/reference/rate-limiting),
  [webhooks](https://rachio.readme.io/reference/webhooks).
- [Offline device functionality](https://support.rachio.com/en_us/offline-device-functionality-SyboPIkFv):
  what runs without the network.
- [Rachio's forum, on a local API](https://community.rachio.com/t/api-call-to-local-sprinkler-ip/4152):
  the staff's answers.
- Rachio's API, the routes: [the account](https://rachio.readme.io/reference/publicpersonid),
  [a device](https://rachio.readme.io/reference/publicdeviceid),
  [the current schedule](https://rachio.readme.io/reference/publicdeviceidcurrent_schedule),
  [starting a zone](https://rachio.readme.io/reference/zonestart),
  [starting several](https://rachio.readme.io/reference/publiczonestart_multiple),
  [a rain delay](https://rachio.readme.io/reference/devicerain_delay).
- [rachiopy](https://github.com/rfverbruggen/rachiopy): the API in use, where the
  documentation contradicts itself.
- [Home Assistant, Rachio](https://www.home-assistant.io/integrations/rachio/): the API
  in use.
- [skeletonkey/rachio](https://github.com/skeletonkey/rachio): a Go client.
