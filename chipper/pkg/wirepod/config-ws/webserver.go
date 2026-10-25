package webserver

import (
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/productivity"
	"github.com/kercre123/wire-pod/chipper/pkg/scripting"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
	"github.com/kercre123/wire-pod/chipper/pkg/wirepod/localization"
	processreqs "github.com/kercre123/wire-pod/chipper/pkg/wirepod/preqs"
	botsetup "github.com/kercre123/wire-pod/chipper/pkg/wirepod/setup"
)

var SttInitFunc func() error

var ProductivityImgPath = "./productivity-images"

const (
	sourceRepoOwner = "kermitine"
	sourceRepoName  = "squire-pod"
)

func apiHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "*")

	switch strings.TrimPrefix(r.URL.Path, "/api/") {
	case "add_custom_intent":
		handleAddCustomIntent(w, r)
	case "edit_custom_intent":
		handleEditCustomIntent(w, r)
	case "get_custom_intents_json":
		handleGetCustomIntentsJSON(w)
	case "remove_custom_intent":
		handleRemoveCustomIntent(w, r)
	case "set_weather_api":
		handleSetWeatherAPI(w, r)
	case "get_weather_api":
		handleGetWeatherAPI(w)
	case "set_kg_api":
		handleSetKGAPI(w, r)
	case "get_kg_api":
		handleGetKGAPI(w)
	case "set_stt_info":
		handleSetSTTInfo(w, r)
	case "get_download_status":
		handleGetDownloadStatus(w)
	case "get_stt_info":
		handleGetSTTInfo(w)
	case "get_config":
		handleGetConfig(w)
	case "get_logs":
		handleGetLogs(w)
	case "get_debug_logs":
		handleGetDebugLogs(w)
	case "is_running":
		handleIsRunning(w)
	case "delete_chats":
		handleDeleteChats(w)
	case "get_ota":
		handleGetOTA(w, r)
	case "get_version_info":
		handleGetVersionInfo(w)
	case "generate_certs":
		handleGenerateCerts(w)
	case "set_productivity_api":
		handleSetProductivityAPI(w, r)
	case "get_productivity_api":
		handleGetProductivityAPI(w)
	case "get_productivity_images":
		handleGetProductivityImages(w)
	case "upload_productivity_images":
		handleUploadProductivityImages(w, r)
	case "delete_productivity_image":
		handleDeleteProductivityImage(w, r)
	case "test_productivity_reminder":
		handleTestProductivityReminder(w, r)
	case "test_nba_reminder":
		handleTestNBAReminder(w, r)
	case "test_nba_final_reminder":
		handleTestNBAFinalReminder(w, r)
	case "test_f1_reminder":
		handleTestF1Reminder(w, r)
	case "test_f1_qualifying_reminder":
		handleTestF1QualifyingReminder(w, r)
	case "test_f1_live_qualifying_reminder":
		handleTestF1LiveQualifyingReminder(w, r)
	case "test_f1_live_practice_reminder":
		handleTestF1LivePracticeReminder(w, r)
	case "is_api_v3":
		fmt.Fprintf(w, "it is!")
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func handleAddCustomIntent(w http.ResponseWriter, r *http.Request) {
	var intent vars.CustomIntent
	if err := json.NewDecoder(r.Body).Decode(&intent); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if anyEmpty(intent.Name, intent.Description, intent.Intent) || len(intent.Utterances) == 0 {
		http.Error(w, "missing required field (name, description, utterances, and intent are required)", http.StatusBadRequest)
		return
	}
	intent.LuaScript = strings.TrimSpace(intent.LuaScript)
	if intent.LuaScript != "" {
		if err := scripting.ValidateLuaScript(intent.LuaScript); err != nil {
			http.Error(w, "lua validation error: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	vars.CustomIntentsExist = true
	vars.CustomIntents = append(vars.CustomIntents, intent)
	saveCustomIntents()
	fmt.Fprint(w, "Intent added successfully.")
}

func handleEditCustomIntent(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Number int `json:"number"`
		vars.CustomIntent
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if request.Number < 1 || request.Number > len(vars.CustomIntents) {
		http.Error(w, "invalid intent number", http.StatusBadRequest)
		return
	}
	intent := &vars.CustomIntents[request.Number-1]
	if request.Name != "" {
		intent.Name = request.Name
	}
	if request.Description != "" {
		intent.Description = request.Description
	}
	if len(request.Utterances) != 0 {
		intent.Utterances = request.Utterances
	}
	if request.Intent != "" {
		intent.Intent = request.Intent
	}
	if request.Params.ParamName != "" {
		intent.Params.ParamName = request.Params.ParamName
	}
	if request.Params.ParamValue != "" {
		intent.Params.ParamValue = request.Params.ParamValue
	}
	if request.Exec != "" {
		intent.Exec = request.Exec
	}
	if request.LuaScript != "" {
		intent.LuaScript = request.LuaScript
		if err := scripting.ValidateLuaScript(intent.LuaScript); err != nil {
			http.Error(w, "lua validation error: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	if len(request.ExecArgs) != 0 {
		intent.ExecArgs = request.ExecArgs
	}
	intent.IsSystemIntent = false
	saveCustomIntents()
	fmt.Fprint(w, "Intent edited successfully.")
}

func handleGetCustomIntentsJSON(w http.ResponseWriter) {
	if !vars.CustomIntentsExist {
		http.Error(w, "you must create an intent first", http.StatusBadRequest)
		return
	}
	customIntentJSONFile, err := os.ReadFile(vars.CustomIntentsPath)
	if err != nil {
		http.Error(w, "could not read custom intents file", http.StatusInternalServerError)
		logger.Println(err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(customIntentJSONFile)
}

func handleRemoveCustomIntent(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Number int `json:"number"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if request.Number < 1 || request.Number > len(vars.CustomIntents) {
		http.Error(w, "invalid intent number", http.StatusBadRequest)
		return
	}
	vars.CustomIntents = append(vars.CustomIntents[:request.Number-1], vars.CustomIntents[request.Number:]...)
	saveCustomIntents()
	fmt.Fprint(w, "Intent removed successfully.")
}

func handleSetWeatherAPI(w http.ResponseWriter, r *http.Request) {
	var config struct {
		Provider string `json:"provider"`
		Key      string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&config); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if config.Provider == "" {
		vars.APIConfig.Weather.Enable = false
	} else {
		vars.APIConfig.Weather.Enable = true
		vars.APIConfig.Weather.Key = strings.TrimSpace(config.Key)
		vars.APIConfig.Weather.Provider = config.Provider
	}
	vars.WriteConfigToDisk()
	fmt.Fprint(w, "Changes successfully applied.")
}

func handleGetWeatherAPI(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(vars.APIConfig.Weather)
}

func handleSetProductivityAPI(w http.ResponseWriter, r *http.Request) {
	// 10MB limit for image uploads
	err := r.ParseMultipartForm(10 << 20)
	if err != nil {
		http.Error(w, "Error parsing form data: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer r.MultipartForm.RemoveAll()
	if len(r.MultipartForm.File["files"]) > 0 {
		http.Error(w, "Upload reminder images through the image library before saving settings", http.StatusBadRequest)
		return
	}

	provider := strings.ToLower(strings.TrimSpace(r.FormValue("provider")))
	key := r.FormValue("key")
	urlVal := r.FormValue("url")
	username := r.FormValue("username")
	password := r.FormValue("password")
	targetRobot := r.FormValue("target_robot")
	reverseFaceSearch := vars.APIConfig.Productivity.ReverseFaceSearch
	if raw := strings.TrimSpace(r.FormValue("reverse_face_search")); raw != "" {
		reverseFaceSearch, err = strconv.ParseBool(raw)
		if err != nil {
			http.Error(w, "Invalid face-search direction", http.StatusBadRequest)
			return
		}
	}
	timezone := strings.TrimSpace(r.FormValue("timezone"))
	nbaConfigStr := strings.TrimSpace(r.FormValue("nba_config"))
	f1ConfigStr := strings.TrimSpace(r.FormValue("f1_config"))
	manualConfig := strings.TrimSpace(r.FormValue("manual_config"))
	if manualConfig == "" {
		manualConfig = "[]"
	}
	var reminders []productivity.ManualReminder
	if err := json.Unmarshal([]byte(manualConfig), &reminders); err != nil {
		http.Error(w, "Invalid manual reminder configuration", http.StatusBadRequest)
		return
	}
	if reminders == nil {
		reminders = []productivity.ManualReminder{}
	}
	for _, reminder := range reminders {
		if reminder.Image != "" && !isSafeProductivityImageName(reminder.Image) {
			http.Error(w, "Invalid image name in manual reminder configuration", http.StatusBadRequest)
			return
		}
	}
	canonicalConfig, err := json.Marshal(reminders)
	if err != nil {
		http.Error(w, "Unable to encode manual reminder configuration", http.StatusInternalServerError)
		return
	}
	manualConfig = string(canonicalConfig)

	// Older clients can save reminders without changing the bedtime schedule.
	bedtimeConfig := vars.APIConfig.Productivity.Bedtime
	if raw := strings.TrimSpace(r.FormValue("bedtime_config")); raw != "" {
		bedtimeConfig = vars.BedtimeConfig{}
		if err := json.Unmarshal([]byte(raw), &bedtimeConfig); err != nil {
			http.Error(w, "Invalid bedtime configuration", http.StatusBadRequest)
			return
		}
		bedtimeConfig.Timezone = strings.TrimSpace(bedtimeConfig.Timezone)
		if err := productivity.ValidateBedtimeConfig(bedtimeConfig); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	var nbaConfig vars.NBAConfig
	if nbaConfigStr != "" {
		if err := json.Unmarshal([]byte(nbaConfigStr), &nbaConfig); err != nil {
			http.Error(w, "Invalid NBA reminder configuration", http.StatusBadRequest)
			return
		}
	}
	nbaConfig.FavoriteTeams = productivity.NormalizeNBATeams(nbaConfig.FavoriteTeams)
	if nbaConfig.PregameMinutes < 1 {
		nbaConfig.PregameMinutes = 15
	}
	if nbaConfig.PregameMinutes > 180 {
		nbaConfig.PregameMinutes = 180
	}
	if nbaConfig.LiveUpdateMinutes < 1 {
		nbaConfig.LiveUpdateMinutes = 5
	}
	if nbaConfig.LiveUpdateMinutes > 60 {
		nbaConfig.LiveUpdateMinutes = 60
	}
	var f1Config vars.F1Config
	if f1ConfigStr != "" {
		if err := json.Unmarshal([]byte(f1ConfigStr), &f1Config); err != nil {
			http.Error(w, "Invalid F1 reminder configuration", http.StatusBadRequest)
			return
		}
	}
	if f1Config.PregameMinutes < 1 {
		f1Config.PregameMinutes = 60
	}
	if f1Config.PregameMinutes > 1440 {
		f1Config.PregameMinutes = 1440
	}
	if f1Config.LiveUpdateMinutes < 1 {
		f1Config.LiveUpdateMinutes = 10
	}
	if f1Config.LiveUpdateMinutes > 60 {
		f1Config.LiveUpdateMinutes = 60
	}
	if f1Config.QualifyingLiveUpdateMinutes < 1 {
		f1Config.QualifyingLiveUpdateMinutes = f1Config.LiveUpdateMinutes
	}
	if f1Config.QualifyingLiveUpdateMinutes > 60 {
		f1Config.QualifyingLiveUpdateMinutes = 60
	}
	if f1Config.PracticeLiveUpdateMinutes < 1 {
		f1Config.PracticeLiveUpdateMinutes = f1Config.LiveUpdateMinutes
	}
	if f1Config.PracticeLiveUpdateMinutes > 60 {
		f1Config.PracticeLiveUpdateMinutes = 60
	}
	previousConfig := vars.APIConfig.Productivity
	vars.APIConfig.Productivity.Enable = provider == "todoist"
	vars.APIConfig.Productivity.Provider = provider
	vars.APIConfig.Productivity.Key = strings.TrimSpace(key)
	vars.APIConfig.Productivity.Url = strings.TrimSpace(urlVal)
	vars.APIConfig.Productivity.Username = strings.TrimSpace(username)
	vars.APIConfig.Productivity.Password = strings.TrimSpace(password)
	vars.APIConfig.Productivity.TargetRobot = strings.TrimSpace(targetRobot)
	vars.APIConfig.Productivity.ReverseFaceSearch = reverseFaceSearch
	vars.APIConfig.Productivity.Timezone = timezone
	vars.APIConfig.Productivity.ManualConfig = manualConfig
	vars.APIConfig.Productivity.NBA = nbaConfig
	vars.APIConfig.Productivity.F1 = f1Config
	vars.APIConfig.Productivity.Bedtime = bedtimeConfig

	if err := vars.WriteConfigToDiskWithError(); err != nil {
		vars.APIConfig.Productivity = previousConfig
		logger.Println("Failed to persist productivity settings: " + err.Error())
		http.Error(w, "Unable to save productivity settings", http.StatusInternalServerError)
		return
	}
	productivity.NotifyConfigUpdated()
	fmt.Fprint(w, "Productivity settings applied.")
}

func handleGetProductivityAPI(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(vars.APIConfig.Productivity)
}

const maxProductivityImageSize = 10 << 20

type productivityImageInfo struct {
	Name     string   `json:"name"`
	Size     int64    `json:"size"`
	Modified int64    `json:"modified"`
	UsedBy   []string `json:"used_by"`
}

func configuredProductivityImageUsage() (map[string][]string, error) {
	usage := make(map[string][]string)
	if strings.TrimSpace(vars.APIConfig.Productivity.ManualConfig) == "" {
		return usage, nil
	}
	var reminders []productivity.ManualReminder
	if err := json.Unmarshal([]byte(vars.APIConfig.Productivity.ManualConfig), &reminders); err != nil {
		return nil, err
	}
	for _, reminder := range reminders {
		if reminder.Image != "" {
			usage[reminder.Image] = append(usage[reminder.Image], reminder.ID)
		}
	}
	return usage, nil
}

func isSupportedProductivityImage(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".png" || ext == ".jpg" || ext == ".jpeg"
}

func isSafeProductivityImageName(name string) bool {
	return name != "" && name != "." && filepath.Base(name) == name && isSupportedProductivityImage(name)
}

func productivityImageInfos() ([]productivityImageInfo, error) {
	if err := os.MkdirAll(ProductivityImgPath, 0755); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(ProductivityImgPath)
	if err != nil {
		return nil, err
	}
	usage, err := configuredProductivityImageUsage()
	if err != nil {
		return nil, fmt.Errorf("read reminder image usage: %w", err)
	}
	images := make([]productivityImageInfo, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !isSupportedProductivityImage(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		usedBy := usage[entry.Name()]
		if usedBy == nil {
			usedBy = []string{}
		}
		images = append(images, productivityImageInfo{
			Name:     entry.Name(),
			Size:     info.Size(),
			Modified: info.ModTime().UnixNano(),
			UsedBy:   usedBy,
		})
	}
	sort.Slice(images, func(i, j int) bool {
		return strings.ToLower(images[i].Name) < strings.ToLower(images[j].Name)
	})
	return images, nil
}

func handleGetProductivityImages(w http.ResponseWriter) {
	images, err := productivityImageInfos()
	if err != nil {
		http.Error(w, "Unable to read productivity image library", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(images)
}

func cleanProductivityImageFilename(name string) string {
	ext := strings.ToLower(filepath.Ext(filepath.Base(name)))
	stem := strings.TrimSuffix(filepath.Base(name), filepath.Ext(filepath.Base(name)))
	var cleaned strings.Builder
	lastDash := false
	for _, char := range stem {
		allowed := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-'
		if allowed {
			cleaned.WriteRune(char)
			lastDash = false
		} else if !lastDash {
			cleaned.WriteByte('-')
			lastDash = true
		}
	}
	stem = strings.Trim(cleaned.String(), "-_")
	if stem == "" {
		stem = "image"
	}
	return stem + ext
}

func nextAvailableProductivityImageName(name string) (string, error) {
	if err := os.MkdirAll(ProductivityImgPath, 0755); err != nil {
		return "", err
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for suffix := 1; ; suffix++ {
		candidate := name
		if suffix > 1 {
			candidate = fmt.Sprintf("%s-%d%s", stem, suffix, ext)
		}
		_, err := os.Stat(filepath.Join(ProductivityImgPath, candidate))
		if os.IsNotExist(err) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
	}
}

func saveProductivityImage(fileHeader *multipart.FileHeader) (productivityImageInfo, error) {
	if fileHeader.Size > maxProductivityImageSize {
		return productivityImageInfo{}, fmt.Errorf("%s exceeds the 10 MB limit", fileHeader.Filename)
	}
	if !isSupportedProductivityImage(fileHeader.Filename) {
		return productivityImageInfo{}, fmt.Errorf("%s must be a PNG or JPEG image", fileHeader.Filename)
	}
	file, err := fileHeader.Open()
	if err != nil {
		return productivityImageInfo{}, err
	}
	defer file.Close()
	_, format, err := image.DecodeConfig(file)
	if err != nil {
		return productivityImageInfo{}, fmt.Errorf("%s is not a valid image", fileHeader.Filename)
	}
	ext := strings.ToLower(filepath.Ext(fileHeader.Filename))
	if format == "png" && ext != ".png" || format == "jpeg" && ext != ".jpg" && ext != ".jpeg" {
		return productivityImageInfo{}, fmt.Errorf("%s extension does not match its image format", fileHeader.Filename)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return productivityImageInfo{}, err
	}
	name, err := nextAvailableProductivityImageName(cleanProductivityImageFilename(fileHeader.Filename))
	if err != nil {
		return productivityImageInfo{}, err
	}
	destination, err := os.OpenFile(filepath.Join(ProductivityImgPath, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return productivityImageInfo{}, err
	}
	written, copyErr := io.Copy(destination, io.LimitReader(file, maxProductivityImageSize+1))
	closeErr := destination.Close()
	if copyErr != nil || closeErr != nil || written > maxProductivityImageSize {
		os.Remove(filepath.Join(ProductivityImgPath, name))
		if copyErr != nil {
			return productivityImageInfo{}, copyErr
		}
		if written > maxProductivityImageSize {
			return productivityImageInfo{}, fmt.Errorf("%s exceeds the 10 MB limit", fileHeader.Filename)
		}
		return productivityImageInfo{}, closeErr
	}
	info, err := os.Stat(filepath.Join(ProductivityImgPath, name))
	if err != nil {
		os.Remove(filepath.Join(ProductivityImgPath, name))
		return productivityImageInfo{}, err
	}
	return productivityImageInfo{Name: name, Size: written, Modified: info.ModTime().UnixNano(), UsedBy: []string{}}, nil
}

func handleUploadProductivityImages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 50<<20)
	if err := r.ParseMultipartForm(50 << 20); err != nil {
		http.Error(w, "Unable to read image upload: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer r.MultipartForm.RemoveAll()
	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		http.Error(w, "Choose at least one PNG or JPEG image", http.StatusBadRequest)
		return
	}
	uploaded := make([]productivityImageInfo, 0, len(files))
	for _, fileHeader := range files {
		imageInfo, err := saveProductivityImage(fileHeader)
		if err != nil {
			for _, saved := range uploaded {
				os.Remove(filepath.Join(ProductivityImgPath, saved.Name))
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		uploaded = append(uploaded, imageInfo)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(uploaded)
}

func handleDeleteProductivityImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var request struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || !isSafeProductivityImageName(request.Name) {
		http.Error(w, "Invalid image name", http.StatusBadRequest)
		return
	}
	usage, err := configuredProductivityImageUsage()
	if err != nil {
		http.Error(w, "Unable to verify whether the image is in use", http.StatusInternalServerError)
		return
	}
	if usedBy := usage[request.Name]; len(usedBy) > 0 {
		http.Error(w, "Image is used by reminder(s): "+strings.Join(usedBy, ", ")+". Remove it from those reminders and save first.", http.StatusConflict)
		return
	}
	if err := os.Remove(filepath.Join(ProductivityImgPath, request.Name)); err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "Image not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Unable to delete image", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleTestProductivityReminder(w http.ResponseWriter, r *http.Request) {
	logger.Println("Received request for /api/test_productivity_reminder")
	err := r.ParseMultipartForm(10 << 20)
	if err != nil {
		logger.Println("Error parsing test form data: " + err.Error())
		http.Error(w, "Error parsing form data: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer r.MultipartForm.RemoveAll()
	if len(r.MultipartForm.File["files"]) > 0 {
		http.Error(w, "Upload reminder images through the image library before testing", http.StatusBadRequest)
		return
	}

	targetRobot := r.FormValue("target_robot")
	if targetRobot == "" {
		http.Error(w, "Target robot is required", http.StatusBadRequest)
		return
	}

	configStr := r.FormValue("reminder_config")
	var reminder productivity.ManualReminder
	if err := json.Unmarshal([]byte(configStr), &reminder); err != nil {
		logger.Println("Error parsing reminder JSON: " + err.Error())
		http.Error(w, "Invalid reminder config", http.StatusBadRequest)
		return
	}
	if reminder.Image != "" && !isSafeProductivityImageName(reminder.Image) {
		http.Error(w, "Invalid reminder image name", http.StatusBadRequest)
		return
	}

	if reminder.Image != "" {
		logger.Println("Test Request uses existing image: " + reminder.Image)
	}

	task := productivity.Task{
		ID:                  reminder.ID,
		RobotESN:            targetRobot,
		Phrases:             reminder.Phrases,
		Image:               reminder.Image,
		Source:              "test",
		RequireConfirmation: reminder.RequireConfirmation,
		SnoozeMinutes:       reminder.SnoozeMinutes,
	}

	productivity.InjectTestTask(task)
	fmt.Fprint(w, "Test reminder queued.")
}

func handleTestNBAReminder(w http.ResponseWriter, r *http.Request) {
	targetRobot := strings.TrimSpace(r.FormValue("target_robot"))
	if err := productivity.InjectTestNBAUpdate(targetRobot); err != nil {
		logger.Println("Unable to queue NBA test update: " + err.Error())
		http.Error(w, "Unable to queue NBA test update: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	fmt.Fprint(w, "Random NBA score update queued.")
}

func handleTestNBAFinalReminder(w http.ResponseWriter, r *http.Request) {
	targetRobot := strings.TrimSpace(r.FormValue("target_robot"))
	if err := productivity.InjectTestNBAFinalUpdate(targetRobot); err != nil {
		logger.Println("Unable to queue NBA final score test: " + err.Error())
		http.Error(w, "Unable to queue NBA final score test: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	fmt.Fprint(w, "Random NBA final score and top performer queued.")
}

func handleTestF1Reminder(w http.ResponseWriter, r *http.Request) {
	targetRobot := strings.TrimSpace(r.FormValue("target_robot"))
	if err := productivity.InjectTestF1Update(targetRobot); err != nil {
		logger.Println("Unable to queue F1 test update: " + err.Error())
		http.Error(w, "Unable to queue F1 test update: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	fmt.Fprint(w, "Live F1 race update queued.")
}

func handleTestF1QualifyingReminder(w http.ResponseWriter, r *http.Request) {
	targetRobot := strings.TrimSpace(r.FormValue("target_robot"))
	if err := productivity.InjectTestF1QualifyingUpdate(targetRobot); err != nil {
		logger.Println("Unable to queue F1 qualifying test: " + err.Error())
		http.Error(w, "Unable to queue F1 qualifying test: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	fmt.Fprint(w, "F1 qualifying result queued.")
}

func handleTestF1LiveQualifyingReminder(w http.ResponseWriter, r *http.Request) {
	targetRobot := strings.TrimSpace(r.FormValue("target_robot"))
	if err := productivity.InjectTestF1LiveQualifyingUpdate(targetRobot); err != nil {
		logger.Println("Unable to queue live F1 qualifying test: " + err.Error())
		http.Error(w, "Unable to queue live F1 qualifying test: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	fmt.Fprint(w, "Live F1 qualifying update queued.")
}

func handleTestF1LivePracticeReminder(w http.ResponseWriter, r *http.Request) {
	targetRobot := strings.TrimSpace(r.FormValue("target_robot"))
	if err := productivity.InjectTestF1LivePracticeUpdate(targetRobot); err != nil {
		logger.Println("Unable to queue live F1 practice test: " + err.Error())
		http.Error(w, "Unable to queue live F1 practice test: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	fmt.Fprint(w, "Live F1 practice update queued.")
}

func handleSetKGAPI(w http.ResponseWriter, r *http.Request) {
	if err := json.NewDecoder(r.Body).Decode(&vars.APIConfig.Knowledge); err != nil {
		fmt.Println(err)
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	vars.WriteConfigToDisk()
	fmt.Fprint(w, "Changes successfully applied.")
}

func handleGetKGAPI(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(vars.APIConfig.Knowledge)
}

func handleSetSTTInfo(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Language string `json:"language"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if vars.APIConfig.STT.Service == "vosk" {
		if !isValidLanguage(request.Language, localization.ValidVoskModels) {
			http.Error(w, "language not valid", http.StatusBadRequest)
			return
		}
		if !localization.VoskModelExists(request.Language) {
			go localization.DownloadVoskModel(request.Language)
			fmt.Fprint(w, "downloading language model...")
			return
		}
	} else if vars.APIConfig.STT.Service == "whisper.cpp" {
		if !isValidLanguage(request.Language, localization.ValidVoskModels) {
			http.Error(w, "language not valid", http.StatusBadRequest)
			return
		}
	} else {
		http.Error(w, "service must be vosk or whisper", http.StatusBadRequest)
		return
	}
	vars.APIConfig.STT.Language = request.Language
	vars.APIConfig.PastInitialSetup = true
	vars.WriteConfigToDisk()
	processreqs.ReloadVosk()
	logger.Println("Reloaded voice processor successfully")
	fmt.Fprint(w, "Language switched successfully.")
}

func handleGetDownloadStatus(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(localization.PollDownloadStatus()))
}

func handleGetSTTInfo(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(vars.APIConfig.STT)
}

func handleGetConfig(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(vars.APIConfig)
}

func handleGetLogs(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(logger.LogList))
}

func handleGetDebugLogs(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(logger.LogTrayList))
}

func handleIsRunning(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte("true"))
}

func handleDeleteChats(w http.ResponseWriter) {
	vars.RememberedChats = []vars.RememberedChat{}
	fmt.Fprint(w, "done")
}

func handleGetOTA(w http.ResponseWriter, r *http.Request) {
	otaName := strings.Split(r.URL.Path, "/")[3]
	targetURL, err := url.Parse("https://archive.org/download/vector-pod-firmware/" + strings.TrimSpace(otaName))
	if err != nil {
		http.Error(w, "failed to parse URL", http.StatusInternalServerError)
		return
	}
	req, err := http.NewRequest(r.Method, targetURL.String(), nil)
	if err != nil {
		http.Error(w, "failed to create request", http.StatusInternalServerError)
		return
	}
	for key, values := range r.Header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "failed to perform request", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	_, err = io.Copy(w, resp.Body)
	if err != nil {
		http.Error(w, "failed to copy response body", http.StatusInternalServerError)
	}
}

func handleGetVersionInfo(w http.ResponseWriter) {
	var installedVer string
	ver, err := os.ReadFile(vars.VersionFile)
	if err == nil {
		installedVer = strings.TrimSpace(string(ver))
	}
	type VersionInfo struct {
		FromSource      bool   `json:"fromsource"`
		InstalledVer    string `json:"installedversion"`
		InstalledCommit string `json:"installedcommit"`
		CurrentVer      string `json:"currentversion"`
		CurrentCommit   string `json:"currentcommit"`
		UpdateAvailable bool   `json:"avail"`
	}
	fromSource := installedVer == ""
	var currentVer, currentCommit string
	var uAvail bool
	if fromSource {
		currentCommit, err = GetLatestCommitSha(sourceRepoOwner, sourceRepoName)
		if err != nil {
			http.Error(w, "error communicating with github (commit): "+err.Error(), http.StatusInternalServerError)
			return
		}
		uAvail = vars.CommitSHA != strings.TrimSpace(currentCommit)
	} else {
		currentVer, err = GetLatestReleaseTag(sourceRepoOwner, sourceRepoName)
		if err != nil {
			http.Error(w, "error communicating with github (ver): "+err.Error(), http.StatusInternalServerError)
			return
		}
		uAvail = installedVer != strings.TrimSpace(currentVer)
	}
	verInfo := VersionInfo{
		FromSource:      fromSource,
		InstalledVer:    installedVer,
		InstalledCommit: vars.CommitSHA,
		CurrentVer:      strings.TrimSpace(currentVer),
		CurrentCommit:   strings.TrimSpace(currentCommit),
		UpdateAvailable: uAvail,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(verInfo)
}

func handleGenerateCerts(w http.ResponseWriter) {
	if err := botsetup.CreateCertCombo(); err != nil {
		http.Error(w, "error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Fprint(w, "done")
}

func saveCustomIntents() {
	customIntentJSONFile, _ := json.Marshal(vars.CustomIntents)
	os.WriteFile(vars.CustomIntentsPath, customIntentJSONFile, 0644)
}

func DisableCachingAndSniffing(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate, max-age=0")
		w.Header().Set("pragma", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Expires", "0")
		next.ServeHTTP(w, r)
	})
}

func StartWebServer() {
	botsetup.RegisterSSHAPI()
	botsetup.RegisterBLEAPI()
	http.HandleFunc("/api/", apiHandler)
	http.HandleFunc("/session-certs/", certHandler)
	http.Handle("/api/productivity-images/", http.StripPrefix("/api/productivity-images/", http.FileServer(http.Dir(ProductivityImgPath))))

	var webRoot http.Handler
	if runtime.GOOS == "darwin" && vars.Packaged {
		appPath, _ := os.Executable()
		webRoot = http.FileServer(http.Dir(filepath.Dir(appPath) + "/../Frameworks/chipper/webroot"))
	} else if runtime.GOOS == "android" || runtime.GOOS == "ios" {
		webRoot = http.FileServer(http.Dir(vars.AndroidPath + "/static/webroot"))
	} else {
		webRoot = http.FileServer(http.Dir("./webroot"))
	}
	http.Handle("/", DisableCachingAndSniffing(webRoot))
	fmt.Printf("Starting webserver at port " + vars.WebPort + " (http://localhost:" + vars.WebPort + ")\n")
	if err := http.ListenAndServe(":"+vars.WebPort, nil); err != nil {
		logger.Println("Error binding to " + vars.WebPort + ": " + err.Error())
		if vars.Packaged {
			logger.ErrMsg("FATAL: Rocket Pod was unable to bind to port " + vars.WebPort + ". Another process is likely using it. Exiting.")
		}
		os.Exit(1)
	}
}

func GetLatestCommitSha(owner, repo string) (string, error) {
	client := &http.Client{}
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits", owner, repo)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to get commits: %s", resp.Status)
	}
	type Commit struct {
		Sha string `json:"sha"`
	}
	var commits []Commit
	if err := json.NewDecoder(resp.Body).Decode(&commits); err != nil {
		return "", err
	}
	if len(commits) == 0 {
		return "", fmt.Errorf("no commits found")
	}
	return commits[0].Sha[:7], nil
}

func GetLatestReleaseTag(owner, repo string) (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", owner, repo)

	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to get latest release: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	type Release struct {
		TagName string `json:"tag_name"`
	}
	var release Release
	if err := json.Unmarshal(body, &release); err != nil {
		return "", err
	}

	return release.TagName, nil
}

func certHandler(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.Contains(r.URL.Path, "/session-certs/"):
		split := strings.Split(r.URL.Path, "/")
		if len(split) < 3 {
			http.Error(w, "must request a cert by esn (ex. /session-certs/00e20145)", http.StatusBadRequest)
			return
		}
		esn := split[2]
		fileBytes, err := os.ReadFile(path.Join(vars.SessionCertPath, esn))
		if err != nil {
			http.Error(w, "cert does not exist", http.StatusNotFound)
			return
		}
		w.Write(fileBytes)
	}
}

func anyEmpty(values ...string) bool {
	for _, v := range values {
		if v == "" {
			return true
		}
	}
	return false
}

func isValidLanguage(language string, validLanguages []string) bool {
	for _, lang := range validLanguages {
		if lang == language {
			return true
		}
	}
	return false
}
