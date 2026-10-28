let eyeOverlayState = null;
let eyeOverlayBusy = false;
const eyeOverlaySerial = new URLSearchParams(window.location.search).get("serial");
const eyeColorStorageKey = "rocket-pod-eye-colors:" + eyeOverlaySerial;

function eyeOverlayMessage(message) {
  document.getElementById("eyeOverlayStatus").textContent = message;
}

function setEyeOverlayBusy(busy) {
  eyeOverlayBusy = busy;
  const supported = eyeOverlayState && eyeOverlayState.supported;
  ["leftEyeColor", "rightEyeColor", "eyeOverlayKey"].forEach(id => {
    document.getElementById(id).disabled = busy;
  });
  document.getElementById("applyEyeOverlay").disabled = busy || !supported;
  document.getElementById("disableEyeOverlay").disabled = busy || !supported || !eyeOverlayState.enabled;
  document.getElementById("checkEyeOverlay").disabled = busy;
}

async function eyeOverlayResponse(response) {
  if (!response.ok) throw new Error((await response.text()).trim());
  return response.json();
}

async function loadEyeOverlayStatus() {
  try {
    eyeOverlayState = await fetch("/api-sdk/eye_overlay_status?serial=" + encodeURIComponent(eyeOverlaySerial), {
      cache: "no-store"
    }).then(eyeOverlayResponse);
    if (eyeOverlayState.supported && eyeOverlayState.enabled) {
      document.querySelectorAll('input[name="eye"]').forEach(input => { input.checked = false; });
    }
    eyeOverlayMessage(!eyeOverlayState.supported
      ? eyeOverlayState.message
      : eyeOverlayState.enabled
        ? "A WireOS eye overlay is active. Applying these colors replaces it; the original custom image is backed up on Vector."
        : "Ready. Choose two colors and select Vector's SSH key to apply them.");
  } catch (error) {
    eyeOverlayState = { supported: false, enabled: false };
    eyeOverlayMessage(error.message);
  }
  setEyeOverlayBusy(eyeOverlayBusy);
}

async function applyDifferentEyeColors() {
  if (eyeOverlayBusy) return;
  const key = document.getElementById("eyeOverlayKey").files[0];
  if (!key) return eyeOverlayMessage("Select Vector's SSH private-key file first.");
  if (key.size > 64 * 1024) return eyeOverlayMessage("The SSH key must be smaller than 64 KB.");
  const left = document.getElementById("leftEyeColor").value;
  const right = document.getElementById("rightEyeColor").value;
  const body = new FormData();
  body.append("enabled", "true");
  body.append("left", left);
  body.append("right", right);
  body.append("key", key);
  setEyeOverlayBusy(true);
  eyeOverlayMessage("Uploading and applying eye colors...");
  try {
    const result = await fetch("/api-sdk/eye_overlay?serial=" + encodeURIComponent(eyeOverlaySerial), {
      method: "POST", body
    }).then(eyeOverlayResponse);
    eyeOverlayState = { supported: true, enabled: true, custom: true };
    document.querySelectorAll('input[name="eye"]').forEach(input => { input.checked = false; });
    try { localStorage.setItem(eyeColorStorageKey, JSON.stringify({ left, right })); } catch (_) { /* Storage can be disabled. */ }
    eyeOverlayMessage(result.message);
  } catch (error) {
    // A failed request may have reached Vector; read his actual state again.
    await loadEyeOverlayStatus();
    eyeOverlayMessage(error.message);
  } finally {
    document.getElementById("eyeOverlayKey").value = "";
    setEyeOverlayBusy(false);
  }
}

async function restoreMatchingEyes() {
  if (eyeOverlayBusy) return false;
  setEyeOverlayBusy(true);
  try {
    const result = await fetch("/api-sdk/eye_overlay?serial=" + encodeURIComponent(eyeOverlaySerial), {
      method: "POST", body: new URLSearchParams({ enabled: "false" })
    }).then(eyeOverlayResponse);
    eyeOverlayState = { supported: true, enabled: false };
    eyeOverlayMessage(result.message);
    if (typeof getCurrentSettings === "function") getCurrentSettings();
    return true;
  } catch (error) {
    eyeOverlayMessage(error.message);
    return false;
  } finally {
    setEyeOverlayBusy(false);
  }
}

async function prepareSharedEyeColor() {
  if (eyeOverlayBusy) return false;
  // Refresh the robot's state: another browser or a restart may have changed it.
  setEyeOverlayBusy(true);
  await loadEyeOverlayStatus();
  setEyeOverlayBusy(false);
  if (eyeOverlayState.supported && eyeOverlayState.enabled) return restoreMatchingEyes();
  return true;
}

async function sendPresetEyeColor(color) {
  if (await prepareSharedEyeColor()) sendForm("/api-sdk/eye_color?color=" + color);
}

try {
  const saved = JSON.parse(localStorage.getItem(eyeColorStorageKey));
  if (saved && /^#[0-9a-f]{6}$/i.test(saved.left) && /^#[0-9a-f]{6}$/i.test(saved.right)) {
    document.getElementById("leftEyeColor").value = saved.left;
    document.getElementById("rightEyeColor").value = saved.right;
  }
} catch (_) { /* Use the default colors when storage is unavailable. */ }
loadEyeOverlayStatus();
