# go-rachio: the design

Research into Rachio irrigation controllers, done on 2026-11-15 from public sources, to
decide what a Go module for them would be. Nothing is built and nothing here was run
against a controller. The decisions at the end are open.

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

## What this module would be

Not decided, and there is one option: **a client of the cloud API**. The account and its
devices, zones and schedules read; zones started with an explicit duration, watering
stopped, a rain delay set; the limit read from the headers. Small.

Events would come by polling, since a webhook needs an address open to the internet.

The Go clients found are stale ([skeletonkey/rachio](https://github.com/skeletonkey/rachio),
last changed in 2023).

## Open questions

- Which controller: a Rachio 3, an earlier generation, or a hose timer. The last is a
  different API.
- Whether anything else already uses the same API key, and so its daily count.

## Not verified

- Nothing above was observed on a controller.
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
- [Home Assistant, Rachio](https://www.home-assistant.io/integrations/rachio/): the API
  in use.
- [skeletonkey/rachio](https://github.com/skeletonkey/rachio): a Go client.
