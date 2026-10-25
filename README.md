# Rocket Pod

`Rocket Pod` is fully-featured server software for the Anki (now Digital Dream Labs) [Vector](https://web.archive.org/web/20190417120536if_/https://www.anki.com/en-us/vector) robot. It was created thanks to Digital Dream Labs' [open-sourced code](https://github.com/digital-dream-labs/chipper).

It allows voice commands to work with any Vector 1.0 or 2.0 for no fee, including regular production robots.

## Installation

The installation guide exists on the wiki: [Installation guide](https://github.com/kercre123/wire-pod/wiki/Installation)

## Repairing voice connections

Open **Bot Setup → Voice connection** and click **Check voice connection**.
For an OSKR/dev-unlocked Vector, select his SSH key file and click
**Repair voice connection** to update his saved server address and certificate.
Both addresses are editable: **Vector IP (SSH destination)** selects the robot
to repair, and **New Rocket Pod server IP (certificate)** sets the address saved
on Vector and included in the server certificate. The latter defaults to the
address used to open Rocket Pod; enter the server's LAN IP if you opened it using
a hostname. A successfully verified new robot IP is saved for future connections.
Vector briefly reconnects, then Rocket
Pod reports the connection-test result. Existing connection files are backed up,
and the uploaded SSH key is not saved.

## Reminder reconnect delay

Reminders wait for 60 seconds of fresh, healthy battery readings after Vector
reconnects or Rocket Pod starts. Offline, unknown, low, or uninitialized battery
readings keep reminders pending. Rocket Pod checks the battery again immediately
before leaving the charger and before approaching for a reminder.

## Wiki

Check out the [wiki](https://github.com/kercre123/wire-pod/wiki) for more information on the upstream project, installation, troubleshooting, development, and generally helpful tips.

## Donate

If you want to :P

[![Buy Me A Coffee](https://www.buymeacoffee.com/assets/img/custom_images/orange_img.png)](https://buymeacoffee.com/kercre123)

## Credits

- [Digital Dream Labs](https://github.com/digital-dream-labs) for open sourcing chipper and creating escape pod (which made this possible)
- [bliteknight](https://github.com/bliteknight) for making the upstream project more accessible with his easy-to-use pre-setup Linux boxes
- [dietb](https://github.com/dietb) for rewriting chipper and giving tips
- [fforchino](https://github.com/fforchino) for adding many features such as localization and multilanguage, and for helping out
- [xanathon](https://github.com/xanathon) for the publicity and web interface help
- Anyone who has opened an issue and/or created a pull request for the upstream project
