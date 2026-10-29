let speechSettings = null;
let speechPollTimer = null;
let speechBusy = false;
const speechDrafts = {};

async function speechJSON(url, options) {
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 15000);
  try {
    const response = await fetch(url, { cache: "no-store", signal: controller.signal, ...options });
    if (!response.ok) throw new Error((await response.text()).trim() || `Request failed (${response.status})`);
    return await response.json();
  } finally {
    clearTimeout(timeout);
  }
}

function setSpeechBusy(busy) {
  speechBusy = busy;
  ["speechProvider", "languageSelection", "speechModel", "speechApply", "speechRefresh"].forEach(id => {
    getE(id).disabled = busy;
  });
  if (!busy) updateSpeechModelDescription();
}

async function loadSpeechSettings() {
  clearTimeout(speechPollTimer);
  setSpeechBusy(true);
  try {
    speechSettings = await speechJSON("/api/get_speech_models");
    const current = speechSettings.current;
    const currentModel = speechSettings.models.find(model => model.id === current.model && model.provider === current.provider);
    getE("speechCurrent").textContent = `Current: ${currentModel ? currentModel.name : current.provider} · ${current.language}`;
    const provider = getE("speechProvider");
    provider.replaceChildren();
    speechSettings.providers.forEach(item => {
      const option = new Option(item.name + (item.available ? "" : " — unavailable"), item.id);
      option.disabled = !item.available;
      provider.add(option);
    });
    getE("speechUnavailable").textContent = speechSettings.providers.filter(item => !item.available).map(item => `${item.name}: ${item.description}`).join(" ");
    provider.value = current.provider;
    if (!provider.value || !speechSettings.providers.some(item => item.id === current.provider && item.available)) {
      provider.value = speechSettings.providers.find(item => item.available)?.id || "";
    }
    getE("languageSelection").value = current.language;
    speechDrafts[`${current.provider}:${current.language}`] = current.model;
    delete getE("speechModel").dataset.selectionKey;
    updateSpeechModels();
    const job = speechSettings.job;
    setSpeechBusy(job.state === "working");
    if (job.state !== "idle") displayMessage("languageStatus", job.message);
    if (job.state === "working") scheduleSpeechPoll();
  } catch (error) {
    displayMessage("languageStatus", `Could not load speech settings: ${error.message}`);
    setSpeechBusy(false);
  }
}

function updateSpeechModels() {
  if (!speechSettings) return;
  const provider = getE("speechProvider").value;
  const language = getE("languageSelection").value;
  const selector = getE("speechModel");
  const oldKey = selector.dataset.selectionKey;
  if (oldKey && selector.value) speechDrafts[oldKey] = selector.value;
  const key = `${provider}:${language}`;
  selector.dataset.selectionKey = key;
  selector.replaceChildren();
  const choices = speechSettings.models.filter(model => model.provider === provider && (!model.language || model.language === language));
  choices.forEach(model => selector.add(new Option(`${model.name} · ${model.size}${model.installed ? " · installed" : ""}`, model.id)));
  const saved = speechDrafts[key] || (provider === "vosk" ? speechSettings.config.vosk_model : speechSettings.config.whisper_model);
  const recommended = provider === "whisper.cpp" ? (language === "en-US" ? "base.en" : "base") : "vosk-model-en-us-0.22-lgraph";
  selector.value = choices.find(model => model.id === saved)?.id || choices.find(model => model.id === recommended)?.id || choices[0]?.id || "";
  getE("speechProviderDescription").textContent = speechSettings.providers.find(item => item.id === provider)?.description || "No compatible local engine is available in this build.";
  updateSpeechModelDescription();
}

function updateSpeechModelDescription() {
  if (!speechSettings) return;
  const model = speechSettings.models.find(item => item.provider === getE("speechProvider").value && item.id === getE("speechModel").value);
  const provider = speechSettings.providers.find(item => item.id === getE("speechProvider").value);
  getE("speechModelDescription").textContent = model ? `${model.description} ${model.installed ? "Already installed; ready to use." : `Download: ${model.size}.`}` : "No model is available for this selection.";
  getE("speechApply").textContent = model?.installed ? "Use installed model" : "Download & use model";
  getE("speechApply").disabled = speechBusy || !model || !provider?.available;
}

async function applySpeechModel() {
  if (speechBusy) return;
  const selection = { provider: getE("speechProvider").value, language: getE("languageSelection").value, model: getE("speechModel").value };
  setSpeechBusy(true);
  displayMessage("languageStatus", "Preparing model… You can leave this page and return to check progress.");
  try {
    const job = await speechJSON("/api/set_speech_model", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(selection) });
    displayMessage("languageStatus", job.message);
    scheduleSpeechPoll();
  } catch (error) {
    displayMessage("languageStatus", error.message);
    setSpeechBusy(false);
  }
}

function scheduleSpeechPoll() {
  clearTimeout(speechPollTimer);
  speechPollTimer = setTimeout(pollSpeechModel, 1000);
}

async function pollSpeechModel() {
  try {
    const job = await speechJSON("/api/get_speech_model_status");
    displayMessage("languageStatus", job.message);
    if (job.state === "working") { scheduleSpeechPoll(); return; }
    setSpeechBusy(false);
    await loadSpeechSettings();
  } catch (error) {
    displayMessage("languageStatus", `Waiting to reconnect: ${error.message}. The server may still be installing the model.`);
    scheduleSpeechPoll();
  }
}
