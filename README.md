# go-rachio

A Go library and command for Rachio irrigation controllers, over Rachio's public cloud
API, the one way into them: the account's controllers with their zones and schedules, what
waters now, the history, and the commands that water a zone, stop, delay for rain and put
a controller in standby. Written against Rachio's documentation and not yet run against
Rachio's servers: [docs/design.md](docs/design.md) says what was read, and where the
documentation contradicts itself.

The command reads the account's API key from `RACHIO_API_KEY` and takes it as no flag. The
key is in the Rachio app's profile, under "API key". With it set:

```bash
just rachio devices            # the account's controllers
just rachio zones              # a controller's zones, by number
just rachio schedules          # its schedules
just rachio status             # what waters now, a rain delay, standby
just rachio water 2 10m        # zone 2 for ten minutes; a zone by number, name or id
just rachio stop               # stop all watering
just rachio rain-delay 36h     # no watering for a day and a half
just rachio off                # standby: no schedule runs; `on` ends it
just rachio run "Summer lawn"  # a schedule now; `skip` skips it
just rachio events --last 72h  # the history
just rachio limit              # how many of the day's requests are left
```

An account with several controllers names one with `--device`, by its name or its id.
Times are in the controller's own zone. Rachio allows 3,500 requests a day for the whole
account; a command is two or three.

[docs/design.md](docs/design.md) is the research the module starts from, what the
controller offers and the ways into it, and the shape of the module with its decisions.
