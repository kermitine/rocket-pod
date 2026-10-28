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

While notifications are queued, Vector searches for a face before each one until
he finds someone. Once he successfully faces that person, the remaining queued
notifications play in order without repeating the search or approach. A new
search is needed after the queue empties, the target robot or settings change,
or a delivery is interrupted. Battery and bedtime checks still apply to each
notification.

In **Productivity**, adjust **Face-search turn speed** from 0.5x to 8.0x and click
**Apply Productivity Settings**. The default 1.0x keeps the original speed.

## Bedtime

Bedtime sends the selected robot to his charger after one minute without user
interaction. Test notifications and voice responses stay awake until they finish,
then receive the full minute of grace. Rocket Pod must remain running and connected.

The logs show the saved schedule, timezone, current local time, whether bedtime
is active, interaction pauses, and go-home attempts. Native sleep requires the
robot's sleep console control. If that control is unavailable, Rocket Pod logs
the failure and keeps him on his charger; it cannot enforce sleep on that firmware.
Sleep requests use the behavior engine on port 8888 and verify the returned
`SleepCycleForceSleep` value. The console can return HTTP 200 even when a setting
does not exist, so the HTTP status alone does not confirm a successful request.
Morning ends bedtime enforcement without forcing him awake.

## Eye colors

Custom red eye colors now wrap the end of the hue wheel back to its start.
This avoids a WireOS animation color-conversion edge case that can show white
eyes during image-based animations while normal eyes remain red. If your red
was saved before this fix, submit it again in **Bot Settings → Eye Color**.

In **Bot Settings → Eye Color → Heterochromia**, choose left and right colors,
select Vector's SSH private-key file, then click **Apply different eye colors**.
Left and right are as you look at Vector. This uses the eye overlay feature in
[WireOS 3.0.1.26 or newer](https://github.com/os-vector/wire-os/releases/tag/3.0.1.26)
and keeps his normal blinking and animated eye shapes. The two colors split the
overlay at its center; extreme expressions can move an eye across that split.

Rocket Pod uploads `/data/data/customFaceOverlay.jpg` and enables the overlay.
An existing custom image is backed up as `customFaceOverlay.jpg.rocket-pod-backup`
before the first replacement. The SSH key is used for the request and is not saved.
Color choices are remembered per robot in this browser. WireOS may reset its
console settings when Vector restarts; reapply the colors here if needed.
**Use matching eyes**, a preset, or submitting the single custom color disables
the overlay and restores the regular shared eye color. Disabling needs no SSH key.

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
