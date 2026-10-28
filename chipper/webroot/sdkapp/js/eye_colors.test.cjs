const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { test } = require("node:test");

async function loadEyeControls({ supported = true, enabled = false, fail = false, rememberedIP, savedKey = false } = {}) {
  const elements = Object.fromEntries([
    "eyeOverlayStatus", "leftEyeColor", "rightEyeColor", "eyeOverlayKey", "eyeOverlayKeyStatus", "eyeOverlayRobotIP",
    "applyEyeOverlay", "disableEyeOverlay", "checkEyeOverlay"
  ].map(id => [id, { value: "", disabled: false, files: [] }]));
  elements.leftEyeColor.value = "#00aaff";
  elements.rightEyeColor.value = "#ff9900";
  Object.defineProperty(elements.eyeOverlayKey, "value", {
    get() { return this.filename || ""; },
    set(value) { this.filename = value; if (!value) this.files = []; }
  });
  const calls = [];
  const sharedColors = [];
  const storage = new Map();
  if (rememberedIP) storage.set("rocket-pod-eye-ip:Vector A", rememberedIP);
  const radios = [{ checked: true }];
  const context = vm.createContext({
    window: { location: { search: "?serial=Vector%20A" } },
    document: {
      getElementById: id => elements[id],
      querySelectorAll: () => radios
    },
    URLSearchParams, FormData,
    localStorage: {
      getItem: key => storage.get(key) || null,
      setItem: (key, value) => storage.set(key, value),
      removeItem: key => storage.delete(key)
    },
    sendForm: url => sharedColors.push(url),
    fetch: async (url, options = {}) => {
      calls.push({ url, ...options });
      if (url.includes("eye_overlay_status")) {
        const ip = new URL(url, "http://rocket-pod").searchParams.get("robot_ip") || "192.0.2.1";
        return { ok: true, json: async () => ({ supported, enabled, robot_ip: ip, key_saved: savedKey, message: "Overlay unavailable" }) };
      }
      if (fail) return { ok: false, text: async () => "WireOS rejected the change" };
      if (options.body && options.body.get("key")) savedKey = true;
      return { ok: true, json: async () => ({ message: "Applied", key_saved: savedKey }) };
    }
  });
  vm.runInContext(fs.readFileSync(path.join(__dirname, "eye_colors.js"), "utf8"), context);
  await new Promise(resolve => setImmediate(resolve));
  return { context, elements, calls, sharedColors, storage, radios };
}

test("applying separate colors uploads and saves the key on the server and clears the browser file", async () => {
  const app = await loadEyeControls();
  const key = new Blob(["private key"]);
  app.elements.eyeOverlayKey.files = [key];
  app.elements.eyeOverlayKey.value = "robot.key";
  await app.context.applyDifferentEyeColors();
  const upload = app.calls.find(call => call.method === "POST");
  assert.equal(upload.url, "/api-sdk/eye_overlay?serial=Vector%20A");
  assert.equal(upload.body.get("enabled"), "true");
  assert.equal(upload.body.get("left"), "#00aaff");
  assert.equal(upload.body.get("right"), "#ff9900");
  assert.equal(await upload.body.get("key").text(), "private key");
  assert.equal(app.elements.eyeOverlayKey.value, "");
  assert.equal(app.radios[0].checked, false);
  assert.equal(app.elements.disableEyeOverlay.disabled, false);
  assert.match(app.elements.eyeOverlayKeyStatus.textContent, /SSH key saved in Rocket Pod/);
  assert.deepEqual(JSON.parse(app.storage.get("rocket-pod-eye-colors:Vector A")), { left: "#00aaff", right: "#ff9900" });
});

test("an upload failure clears the key, refreshes state, and does not save colors", async () => {
  const app = await loadEyeControls({ fail: true });
  app.elements.eyeOverlayKey.files = [new Blob(["private key"])];
  app.elements.eyeOverlayKey.value = "robot.key";
  await app.context.applyDifferentEyeColors();
  assert.equal(app.elements.eyeOverlayKey.value, "");
  assert.equal(app.storage.size, 0);
  assert.equal(app.calls.filter(call => call.url.includes("eye_overlay_status")).length, 2);
  assert.equal(app.elements.eyeOverlayStatus.textContent, "WireOS rejected the change");
});

test("missing and oversized SSH keys do not start an upload", async () => {
  const app = await loadEyeControls();
  await app.context.applyDifferentEyeColors();
  app.elements.eyeOverlayKey.files = [{ size: 65537 }];
  await app.context.applyDifferentEyeColors();
  assert.equal(app.calls.length, 1);
});

test("a preset turns off the overlay before changing the shared color", async () => {
  const app = await loadEyeControls({ enabled: true });
  app.elements.eyeOverlayKey.files = [new Blob(["private key"])];
  await app.context.sendPresetEyeColor(4);
  const disable = app.calls.find(call => call.method === "POST");
  assert.equal(disable.body.get("enabled"), "false");
  assert.equal(await disable.body.get("key").text(), "private key");
  assert.equal(app.elements.eyeOverlayKey.value, "");
  assert.equal(app.elements.eyeOverlayKey.files.length, 0);
  assert.deepEqual(app.sharedColors, ["/api-sdk/eye_color?color=4"]);
});

test("a failed overlay disable prevents a misleading shared color change", async () => {
  const app = await loadEyeControls({ enabled: true, fail: true });
  app.elements.eyeOverlayKey.files = [new Blob(["private key"])];
  await app.context.sendPresetEyeColor(4);
  assert.equal(app.sharedColors.length, 0);
  assert.equal(app.elements.eyeOverlayStatus.textContent, "WireOS rejected the change");
});

test("standard eye colors remain available on firmware without overlays", async () => {
  const app = await loadEyeControls({ supported: false });
  assert.equal(app.elements.applyEyeOverlay.disabled, true);
  await app.context.sendPresetEyeColor(2);
  assert.equal(app.calls.filter(call => call.method === "POST").length, 0);
  assert.deepEqual(app.sharedColors, ["/api-sdk/eye_color?color=2"]);
});

test("an IP override is checked and used for uploading and disabling the overlay", async () => {
  const app = await loadEyeControls({ enabled: true });
  assert.equal(app.elements.eyeOverlayRobotIP.placeholder, "192.0.2.1");
  assert.equal(app.elements.eyeOverlayRobotIP.value, "");
  app.elements.eyeOverlayRobotIP.value = " 192.0.2.42 ";
  app.context.eyeOverlayDestinationChanged();
  assert.equal(app.elements.applyEyeOverlay.disabled, true);
  assert.equal(app.elements.disableEyeOverlay.disabled, true);
  assert.equal(app.elements.eyeOverlayRobotIP.disabled, false);
  await app.context.loadEyeOverlayStatus();
  assert.equal(new URL(app.calls.at(-1).url, "http://rocket-pod").searchParams.get("robot_ip"), "192.0.2.42");
  assert.equal(app.elements.applyEyeOverlay.disabled, false);
  app.elements.eyeOverlayKey.files = [new Blob(["private key"])];
  await app.context.applyDifferentEyeColors();
  await app.context.restoreMatchingEyes();
  const changes = app.calls.filter(call => call.method === "POST");
  assert.deepEqual(changes.map(call => call.body.get("robot_ip")), ["192.0.2.42", "192.0.2.42"]);
  assert.equal(app.storage.get("rocket-pod-eye-ip:Vector A"), "192.0.2.42");
});

test("a remembered IP is loaded and clearing it restores the saved robot address", async () => {
  const app = await loadEyeControls({ rememberedIP: "192.0.2.42" });
  assert.equal(app.elements.eyeOverlayRobotIP.value, "192.0.2.42");
  assert.equal(new URL(app.calls[0].url, "http://rocket-pod").searchParams.get("robot_ip"), "192.0.2.42");
  app.elements.eyeOverlayRobotIP.value = "";
  app.context.eyeOverlayDestinationChanged();
  await app.context.loadEyeOverlayStatus();
  assert.equal(new URL(app.calls.at(-1).url, "http://rocket-pod").searchParams.has("robot_ip"), false);
  assert.equal(app.elements.eyeOverlayRobotIP.placeholder, "192.0.2.1");
  assert.equal(app.storage.has("rocket-pod-eye-ip:Vector A"), false);
});

test("an unsuccessful connection check keeps the address editable and does not remember it", async () => {
  const app = await loadEyeControls({ supported: false });
  app.elements.eyeOverlayRobotIP.value = "192.0.2.42";
  await app.context.loadEyeOverlayStatus();
  assert.equal(app.elements.eyeOverlayRobotIP.disabled, false);
  assert.equal(app.elements.applyEyeOverlay.disabled, true);
  assert.equal(app.storage.size, 0);
});

test("restoring matching eyes or selecting a preset requires the key before changing a saved overlay", async () => {
  const app = await loadEyeControls({ enabled: true });
  assert.equal(await app.context.restoreMatchingEyes(), false);
  await app.context.sendPresetEyeColor(4);
  assert.equal(app.calls.filter(call => call.method === "POST").length, 0);
  assert.equal(app.sharedColors.length, 0);
  assert.match(app.elements.eyeOverlayStatus.textContent, /SSH private-key.*once/);
  app.elements.eyeOverlayKey.files = [{ size: 65537 }];
  assert.equal(await app.context.restoreMatchingEyes(), false);
  assert.equal(app.calls.filter(call => call.method === "POST").length, 0);
});

test("a saved server key is reused after page reload for applying and restoring eyes", async () => {
  const app = await loadEyeControls({ enabled: true, savedKey: true });
  assert.match(app.elements.eyeOverlayKeyStatus.textContent, /SSH key saved in Rocket Pod/);
  await app.context.applyDifferentEyeColors();
  await app.context.restoreMatchingEyes();
  const changes = app.calls.filter(call => call.method === "POST");
  assert.equal(changes.length, 2);
  assert.equal(changes.every(call => !call.body.has("key")), true);
  assert.deepEqual(changes.map(call => call.body.get("enabled")), ["true", "false"]);
  assert.match(app.elements.eyeOverlayKeyStatus.textContent, /SSH key saved in Rocket Pod/);
});

test("a preset uses the saved key to disable an overlay without another upload", async () => {
  const app = await loadEyeControls({ enabled: true, savedKey: true });
  await app.context.sendPresetEyeColor(4);
  const disable = app.calls.find(call => call.method === "POST");
  assert.equal(disable.body.get("enabled"), "false");
  assert.equal(disable.body.has("key"), false);
  assert.deepEqual(app.sharedColors, ["/api-sdk/eye_color?color=4"]);
});

test("uploading a replacement takes precedence over the saved key", async () => {
  const app = await loadEyeControls({ savedKey: true });
  app.elements.eyeOverlayKey.files = [new Blob(["replacement key"])];
  await app.context.applyDifferentEyeColors();
  const upload = app.calls.find(call => call.method === "POST");
  assert.equal(await upload.body.get("key").text(), "replacement key");
  assert.equal(app.elements.eyeOverlayKey.files.length, 0);
  assert.match(app.elements.eyeOverlayKeyStatus.textContent, /SSH key saved in Rocket Pod/);
});

test("the custom color picker wraps rounded red to zero before sending", async () => {
  const mainSource = fs.readFileSync(path.join(__dirname, "main.js"), "utf8");
  const start = mainSource.indexOf("async function sendCustomColor()");
  const end = mainSource.indexOf("function updateStats()", start);
  assert.ok(start >= 0 && end > start, "custom color submission function was not found");
  for (const [hue, expected] of [[359.99, "0.000"], [360, "0.000"], [0, "0.000"], [180, "0.500"], [359, "0.997"]]) {
    let request;
    const context = vm.createContext({
      prepareSharedEyeColor: async () => true,
      colorPicker: { color: { hue, saturation: 100 } },
      esn: "test-robot",
      XMLHttpRequest: class {
        open(method, url) { this.method = method; this.url = url; }
        setRequestHeader() {}
        send(body) { request = { method: this.method, url: this.url, body: new URLSearchParams(body) }; }
      }
    });
    vm.runInContext(mainSource.slice(start, end), context);
    await context.sendCustomColor();
    assert.equal(request.method, "POST");
    assert.equal(request.url, "/api-sdk/custom_eye_color?serial=test-robot");
    assert.equal(request.body.get("hue"), expected, `picker hue ${hue}`);
    assert.equal(request.body.get("sat"), "1.000");
  }
});
