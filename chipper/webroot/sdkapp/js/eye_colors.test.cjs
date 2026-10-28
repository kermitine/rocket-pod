const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { test } = require("node:test");

async function loadEyeControls({ supported = true, enabled = false, fail = false } = {}) {
  const elements = Object.fromEntries([
    "eyeOverlayStatus", "leftEyeColor", "rightEyeColor", "eyeOverlayKey",
    "applyEyeOverlay", "disableEyeOverlay", "checkEyeOverlay"
  ].map(id => [id, { value: "", disabled: false, files: [] }]));
  elements.leftEyeColor.value = "#00aaff";
  elements.rightEyeColor.value = "#ff9900";
  const calls = [];
  const sharedColors = [];
  const storage = new Map();
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
      setItem: (key, value) => storage.set(key, value)
    },
    sendForm: url => sharedColors.push(url),
    fetch: async (url, options = {}) => {
      calls.push({ url, ...options });
      if (url.includes("eye_overlay_status")) {
        return { ok: true, json: async () => ({ supported, enabled, message: "Overlay unavailable" }) };
      }
      if (fail) return { ok: false, text: async () => "WireOS rejected the change" };
      return { ok: true, json: async () => ({ message: "Applied" }) };
    }
  });
  vm.runInContext(fs.readFileSync(path.join(__dirname, "eye_colors.js"), "utf8"), context);
  await new Promise(resolve => setImmediate(resolve));
  return { context, elements, calls, sharedColors, storage, radios };
}

test("applying separate colors uploads the key, remembers only colors, and clears the key", async () => {
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
  await app.context.sendPresetEyeColor(4);
  const disable = app.calls.find(call => call.method === "POST");
  assert.equal(disable.body.get("enabled"), "false");
  assert.deepEqual(app.sharedColors, ["/api-sdk/eye_color?color=4"]);
});

test("a failed overlay disable prevents a misleading shared color change", async () => {
  const app = await loadEyeControls({ enabled: true, fail: true });
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
