function updateSSHStatus(statusString) {
  setupStatus = document.getElementById("oskrSetupProgress");
  setupStatus.innerHTML = "";
  setupStatusP = document.createElement("p");
  setupStatusP.innerHTML = statusString;
  setupStatus.appendChild(setupStatusP);
}

async function doSSHSetup() {
  const ip = document.getElementById("sshIp").value.trim();
  const serial = document.getElementById("sshSavedRobot").value;
  const key = document.getElementById("sshKeyFile").files[0];

  if (ip && (key || serial)) {
    if (key && key.size > 64 * 1024) return updateSSHStatus("The SSH key must be smaller than 64 KB.");
    const formData = new FormData();
    if (key) formData.append("key", key);
    formData.append("ip", ip);
    formData.append("serial", serial);

    try {
      const result = await fetch("/api-ssh/setup", {
        method: "POST",
        body: formData,
      });
      const response = await result.text();
      if (result.ok && response === "running") {
        document.getElementById("oskrSetup").style.display = "none";
        updateSSHSetup();
        return;
      }
      updateSSHStatus(response);
    } catch (error) {
      updateSSHStatus(error.message);
    } finally {
      document.getElementById("sshKeyFile").value = "";
    }
  } else {
    updateSSHStatus("Enter Vector's IP address, then select a saved Vector or upload the SSH key once.");
  }
}

function updateSSHKeySelection() {
  const selected = document.getElementById("sshSavedRobot").selectedOptions[0];
  if (selected && selected.value) document.getElementById("sshIp").value = selected.dataset.ip;
  document.getElementById("sshKeyFile").value = "";
}

function loadSavedSSHRobots(robots) {
  const select = document.getElementById("sshSavedRobot");
  const previous = select.value;
  const placeholder = document.createElement("option");
  placeholder.value = "";
  placeholder.textContent = "New Vector / upload a key";
  select.replaceChildren(placeholder);
  robots.filter(robot => robot.key_saved).forEach(robot => {
    const option = document.createElement("option");
    option.value = robot.esn;
    option.dataset.ip = robot.ip_address;
    option.textContent = `${robot.esn} (${robot.ip_address}) (saved SSH key)`;
    select.appendChild(option);
  });
  if (Array.from(select.options).some(option => option.value === previous)) select.value = previous;
}

function updateSSHSetup() {
  interval = setInterval(function () {
    fetch("/api-ssh/get_setup_status")
      .then((response) => response.text())
      .then((response) => {
        statusText = response;
        if (response.includes("done")) {
          updateSSHStatus(
            "File transfer complete! Use the above section to complete bot setup. The bot should eventually be on the onboarding screen."
          );
          document.getElementById("oskrSetup").style.display = "block";
          clearInterval(interval);
          loadVoiceRepair();
        } else if (response.includes("error")) {
          resp = response;
          if (response.includes("no route to host")) {
            resp =
              "Rocket Pod was unable to connect to the robot. Make sure the robot is running OSKR/dev software and that it is on the same network as this Rocket Pod instance. Also double-check the IP.";
          }
          updateSSHStatus(resp);
          clearInterval(interval);
          document.getElementById("oskrSetup").style.display = "block";
          return;
        } else if (response.includes("not running")) {
          updateSSHStatus("Initiating SSH transfer...");
        } else {
          updateSSHStatus(response);
        }
      });
  }, 500);
}

let voiceRepairBusy = false;

function setVoiceRepairBusy(busy) {
  voiceRepairBusy = busy;
  ["voiceCheckButton", "voiceRepairButton", "voiceRepairRobot", "voiceRepairRobotIP", "voiceRepairServerIP", "voiceRepairKey"].forEach(id => {
    document.getElementById(id).disabled = busy;
  });
}

async function voiceRepairResponse(response) {
  if (!response.ok) throw new Error(await response.text());
  return response.json();
}

async function loadVoiceRepair() {
  if (voiceRepairBusy) return;
  try {
    const [info, config, status] = await Promise.all([
      fetch("/api-ssh/robots", { cache: "no-store" }).then(voiceRepairResponse),
      fetch("/api/get_config").then(voiceRepairResponse),
      fetch("/api-ssh/voice_repair_status").then(voiceRepairResponse)
    ]);
    const select = document.getElementById("voiceRepairRobot");
    const previous = select.value;
    loadSavedSSHRobots(info.robots || []);
    select.replaceChildren();
    (info.robots || []).forEach(robot => {
      const option = document.createElement("option");
      option.value = robot.esn;
      option.textContent = `${robot.esn} (${robot.ip_address})`;
      option.dataset.ip = robot.ip_address;
      option.dataset.keySaved = robot.key_saved ? "true" : "false";
      select.appendChild(option);
    });
    if (Array.from(select.options).some(option => option.value === previous)) select.value = previous;
    if (!document.getElementById("voiceRepairRobotIP").value || select.value !== previous) updateVoiceRepairRobotIP();
    updateVoiceRepairKeyStatus();
    const ip = document.getElementById("voiceRepairServerIP");
    if (!ip.value) ip.value = window.location.hostname.replace(/^\[|\]$/g, "");
    document.getElementById("voiceRepairIPField").hidden = config.server.epconfig;
    if (status.running) {
      setVoiceRepairBusy(true);
      displayMessage("voiceRepairStatus", status.message);
      pollVoiceRepair();
    } else if (!select.value) {
      displayMessage("voiceRepairStatus", "Authenticate Vector below first.");
    } else if (status.message) {
      displayMessage("voiceRepairStatus", status.error || status.message);
    }
  } catch (error) {
    displayMessage("voiceRepairStatus", error.message);
  }
}

function updateVoiceRepairRobotIP() {
  const selected = document.getElementById("voiceRepairRobot").selectedOptions[0];
  document.getElementById("voiceRepairRobotIP").value = selected ? selected.dataset.ip : "";
  document.getElementById("voiceRepairKey").value = "";
  updateVoiceRepairKeyStatus();
}

function updateVoiceRepairKeyStatus() {
  const selected = document.getElementById("voiceRepairRobot").selectedOptions[0];
  displayMessage("voiceRepairKeyStatus", selected && selected.dataset.keySaved === "true"
    ? "Saved SSH key available. Leave the file empty to reuse it, or upload a replacement. This key is shared with eye colors and bot setup."
    : "Upload the SSH key once. After a successful connection it will be saved for voice repair, eye colors, and bot setup.");
}

async function checkVoiceConnection() {
  const serial = document.getElementById("voiceRepairRobot").value;
  if (!serial) return displayMessage("voiceRepairStatus", "Authenticate Vector below first.");
  setVoiceRepairBusy(true);
  displayMessage("voiceRepairStatus", "Checking Vector's voice connection...");
  try {
    const result = await fetch("/api-ssh/check_voice", {
      method: "POST", body: new URLSearchParams({ serial, robot_ip: document.getElementById("voiceRepairRobotIP").value.trim() })
    }).then(voiceRepairResponse);
    displayMessage("voiceRepairStatus", result.error || result.message);
  } catch (error) {
    displayMessage("voiceRepairStatus", error.message);
  } finally {
    setVoiceRepairBusy(false);
  }
}

async function repairVoiceConnection() {
  const serial = document.getElementById("voiceRepairRobot").value;
  const key = document.getElementById("voiceRepairKey").files[0];
  if (!serial) return displayMessage("voiceRepairStatus", "Authenticate Vector below first.");
  const selected = document.getElementById("voiceRepairRobot").selectedOptions[0];
  if (!key && (!selected || selected.dataset.keySaved !== "true")) return displayMessage("voiceRepairStatus", "Select Vector's SSH key file once to save it in Rocket Pod.");
  if (key && key.size > 64 * 1024) return displayMessage("voiceRepairStatus", "The SSH key must be smaller than 64 KB.");
  const body = new FormData();
  body.append("serial", serial);
  body.append("robot_ip", document.getElementById("voiceRepairRobotIP").value.trim());
  body.append("server_ip", document.getElementById("voiceRepairServerIP").value.trim());
  if (key) body.append("key", key);
  setVoiceRepairBusy(true);
  displayMessage("voiceRepairStatus", "Starting voice connection repair...");
  try {
    const status = await fetch("/api-ssh/repair_voice", { method: "POST", body }).then(voiceRepairResponse);
    document.getElementById("voiceRepairKey").value = "";
    displayMessage("voiceRepairStatus", status.message);
    pollVoiceRepair();
  } catch (error) {
    displayMessage("voiceRepairStatus", error.message);
    setVoiceRepairBusy(false);
  } finally {
    document.getElementById("voiceRepairKey").value = "";
  }
}

async function pollVoiceRepair() {
  try {
    const status = await fetch("/api-ssh/voice_repair_status").then(voiceRepairResponse);
    displayMessage("voiceRepairStatus", status.error || status.message);
    if (status.running) {
      setTimeout(pollVoiceRepair, 1000);
    } else {
      setVoiceRepairBusy(false);
      await loadVoiceRepair();
    }
  } catch (error) {
    displayMessage("voiceRepairStatus", `Unable to read repair progress: ${error.message}. Reopen Bot Setup to check again.`);
    setVoiceRepairBusy(false);
  }
}
