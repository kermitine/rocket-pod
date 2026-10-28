# Rocket Pod

`Rocket Pod` is fully-featured server software for the Anki (now Digital Dream Labs) [Vector](https://web.archive.org/web/20190417120536if_/https://www.anki.com/en-us/vector) robot. It was created thanks to Digital Dream Labs' [open-sourced code](https://github.com/digital-dream-labs/chipper).

It allows voice commands to work with any Vector 1.0 or 2.0 for no fee, including regular production robots.

## Installation

The installation guide exists on the wiki: [Installation guide](https://github.com/kercre123/wire-pod/wiki/Installation)

## Repairing voice connections

Open **Bot Setup → Voice connection** and click **Check voice connection**.
For an OSKR/dev-unlocked Vector, reuse his saved SSH key or upload it once, then click
**Repair voice connection** to update his saved server address and certificate.
Both addresses are editable: **Vector IP (SSH destination)** selects the robot
to repair, and **New Rocket Pod server IP (certificate)** sets the address saved
on Vector and included in the server certificate. The latter defaults to the
address used to open Rocket Pod; enter the server's LAN IP if you opened it using
a hostname. A successfully verified new robot IP is saved for future connections.
Vector briefly reconnects, then Rocket
Pod reports the connection-test result. Existing connection files are backed up,
and the SSH key is saved after a successful connection. Eye colors, voice repair,
and initial SSH bot setup share the same saved key per Vector. Existing keys
saved through eye colors are reused automatically. In **Configure an OSKR/dev-unlocked
robot**, select a Vector with a saved key, or upload it once for a new robot.
Initial setup reads Vector's serial over SSH so that key becomes available to
the other features after authentication. Uploading a new key in any of these
features replaces the shared key only after a successful SSH connection and
robot-identity check. Saved keys survive Rocket Pod and host restarts.

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
before the first replacement. After a successful SSH connection, Rocket Pod saves
the key per robot in its persistent `jdocs/ssh-keys` directory. Eye-color
changes, restoring matching eyes, voice repair, and SSH bot setup reuse that key, including after Rocket Pod
or its host restarts. The UI shows whether a key is saved. To replace it, select
a new key when applying colors or restoring matching eyes; a failed SSH connection
keeps the previous key. Key files use owner-only permissions where supported,
are excluded from Git, and are stored in the existing persistent Docker volume.
The overlay settings are saved in Vector's `consoleVarsAnim.ini`, which
[WireOS loads before starting the animation engine](https://github.com/os-vector/wire-os-victor/blob/main/animProcess/src/cozmoAnim/cozmoAnimMain.cpp#L149).
Colors survive a reboot even while Rocket Pod is offline. Apply the colors once
after updating Rocket Pod to save any previously applied colors for reboot.
Only the eye-overlay settings are updated; other saved animation settings are
preserved, and the original settings file is backed up before its first change.
Color picker choices are also remembered per robot in this browser.
**Vector IP (eye controls and SSH destination)** overrides the selected robot's
saved address for checking, applying, and disabling the overlay. Leave it blank
to use the saved address. After editing, click **Check connection again**. A
working override is remembered per robot in this browser; it does not change
the address saved in Bot Setup.
**Use matching eyes**, a preset, or submitting the single custom color disables
the overlay and saves that choice for reboot using the saved SSH key. If no key
has been saved yet, select it once before changing an active overlay.

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
