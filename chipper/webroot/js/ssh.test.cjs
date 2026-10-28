const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { test } = require("node:test");

async function loadSSHControls({ robots = [
  { esn: "00aa0001", ip_address: "192.0.2.1", key_saved: true },
  { esn: "00aa0002", ip_address: "192.0.2.2", key_saved: false }
] } = {}) {
  class Element {
    constructor() { this._value = ""; this.children = []; this.dataset = {}; this.files = []; this.style = {}; }
    get value() { return this._value; }
    set value(value) { this._value = value; if (!value) this.files = []; }
    get options() { return this.children; }
    get selectedOptions() { return this.children.filter(option => option.value === this.value); }
    replaceChildren(...children) { this.children = children; this.value = children[0]?.value || ""; }
    appendChild(child) { this.children.push(child); if (this.children.length === 1) this.value = child.value; }
  }
  const elements = Object.fromEntries([
    "sshIp", "sshSavedRobot", "sshKeyFile", "oskrSetup", "oskrSetupProgress",
    "voiceRepairRobot", "voiceRepairRobotIP", "voiceRepairServerIP", "voiceRepairKey",
    "voiceRepairIPField", "voiceRepairKeyStatus", "voiceRepairStatus", "voiceCheckButton", "voiceRepairButton"
  ].map(id => [id, new Element()]));
  const calls = [];
  const context = vm.createContext({
    document: { getElementById: id => elements[id], createElement: () => new Element() },
    window: { location: { hostname: "192.0.2.100" } },
    FormData, URLSearchParams,
    displayMessage: (id, text) => { elements[id].textContent = text; },
    setInterval: () => 1, clearInterval() {}, setTimeout() {},
    fetch: async (url, options = {}) => {
      calls.push({ url, ...options });
      if (url === "/api-ssh/robots") return { ok: true, json: async () => ({ robots }) };
      if (url === "/api/get_config") return { ok: true, json: async () => ({ server: { epconfig: false } }) };
      if (url === "/api-ssh/voice_repair_status") return { ok: true, json: async () => ({ running: false, message: "" }) };
      if (url === "/api-ssh/repair_voice") return { ok: true, json: async () => ({ running: true, message: "Connecting" }) };
      if (url === "/api-ssh/setup") return { ok: true, text: async () => "running" };
      throw new Error("Unexpected URL: " + url);
    }
  });
  vm.runInContext(fs.readFileSync(path.join(__dirname, "ssh.js"), "utf8"), context);
  await context.loadVoiceRepair();
  return { context, elements, calls };
}

test("voice repair reuses a shared key without uploading another file", async () => {
  const app = await loadSSHControls();
  assert.match(app.elements.voiceRepairKeyStatus.textContent, /Saved SSH key available/);
  await app.context.repairVoiceConnection();
  const repair = app.calls.find(call => call.url === "/api-ssh/repair_voice");
  assert.equal(repair.body.get("serial"), "00aa0001");
  assert.equal(repair.body.get("robot_ip"), "192.0.2.1");
  assert.equal(repair.body.has("key"), false);
});

test("bot setup selects an existing shared key and supports an IP override", async () => {
  const app = await loadSSHControls();
  assert.equal(app.elements.sshSavedRobot.options.length, 2);
  app.elements.sshSavedRobot.value = "00aa0001";
  app.context.updateSSHKeySelection();
  assert.equal(app.elements.sshIp.value, "192.0.2.1");
  app.elements.sshIp.value = "192.0.2.42";
  await app.context.doSSHSetup();
  const setup = app.calls.find(call => call.url === "/api-ssh/setup");
  assert.equal(setup.body.get("serial"), "00aa0001");
  assert.equal(setup.body.get("ip"), "192.0.2.42");
  assert.equal(setup.body.has("key"), false);
});

test("both SSH tools accept a replacement upload and clear the browser file", async () => {
  const app = await loadSSHControls();
  app.elements.voiceRepairKey.files = [new Blob(["replacement key"])];
  await app.context.repairVoiceConnection();
  const repair = app.calls.find(call => call.url === "/api-ssh/repair_voice");
  assert.equal(await repair.body.get("key").text(), "replacement key");
  assert.equal(app.elements.voiceRepairKey.files.length, 0);
  app.elements.sshIp.value = "192.0.2.99";
  app.elements.sshKeyFile.files = [new Blob(["new robot key"])];
  await app.context.doSSHSetup();
  const setup = app.calls.find(call => call.url === "/api-ssh/setup");
  assert.equal(setup.body.get("serial"), "");
  assert.equal(await setup.body.get("key").text(), "new robot key");
  assert.equal(app.elements.sshKeyFile.files.length, 0);
});

test("changing robots clears a selected key and does not reuse another robot's saved key", async () => {
  const app = await loadSSHControls();
  app.elements.voiceRepairKey.files = [new Blob(["wrong robot key"])];
  app.elements.voiceRepairRobot.value = "00aa0002";
  app.context.updateVoiceRepairRobotIP();
  assert.equal(app.elements.voiceRepairKey.files.length, 0);
  assert.match(app.elements.voiceRepairKeyStatus.textContent, /Upload the SSH key once/);
  await app.context.repairVoiceConnection();
  assert.equal(app.calls.some(call => call.url === "/api-ssh/repair_voice"), false);
});

test("a new installation can upload a key for initial setup before any robot is authenticated", async () => {
  const app = await loadSSHControls({ robots: [] });
  assert.equal(app.elements.sshSavedRobot.options.length, 1);
  app.elements.sshIp.value = "192.0.2.99";
  app.elements.sshKeyFile.files = [new Blob(["first robot key"])];
  await app.context.doSSHSetup();
  assert.equal(app.calls.some(call => call.url === "/api-ssh/setup"), true);
});
