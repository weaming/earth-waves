package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Settings 定义了网站的全局配置
type Settings struct {
	Domain string `json:"domain"`
}

// AboutContent 定义了“关于”页面的数据结构
type AboutContent struct {
	Content string `json:"Content"`
	Email   string `json:"Email"`
}

// AboutPageData 用于向 about.html 模板传递数据和上下文
type AboutPageData struct {
	AboutContent
	IsAdmin bool
}

//go:embed templates/*
var templateFS embed.FS

// specialJsonFiles 列出了所有非音频元数据的特殊 JSON 文件，在处理时需要跳过
var specialJsonFiles = []string{"about.json", "settings.json"}

// AudioMetadata 定义了音频文件的元数据结构
type AudioMetadata struct {
	SourceFilename       string    `json:"source_filename"`
	Title                string    `json:"title"`
	Description          string    `json:"description"`
	Location             string    `json:"location"`
	RecordDate           time.Time `json:"record_date"`
	DurationSeconds      float64   `json:"duration_seconds"`
	SourceFileSizeMB     float64   `json:"source_file_size_mb"`     // 源文件大小(MB)
	CompressedFileSizeMB float64   `json:"compressed_file_size_mb"` // 压缩后文件大小(MB)
	CompressedAudioPath  string    `json:"compressed_audio_path"`   // 相对于dist目录的路径
	TechInfo             struct {
		SampleRate   int  `json:"sample_rate"`
		BitDepth     int  `json:"bit_depth"`
		Channels     int  `json:"channels"`
		IsCompressed bool `json:"is_compressed"`
	} `json:"tech_info"`
}

var (
	wavDir         string
	jsonDir        string
	m4aDir         string
	distDir        = "dist"
	assetsAudioDir = "dist/assets/audio"
	staticDir      = "static"
	timeRegex      = regexp.MustCompile(`(\d{8}_\d{6}|\d{6}_\d{6})`)
)

func main() {
	// --- 命令行参数处理 ---
	wavPathFlag := flag.String("wav", "", "Path to the directory containing WAV files (required)")
	genFlag := flag.Bool("gen", false, "Generate static site directly without starting the server")
	flag.Parse()

	if *wavPathFlag == "" {
		fmt.Println("WAV directory path is required. Use the -wav flag.")
		flag.Usage()
		os.Exit(1)
	}

	wavDir = *wavPathFlag
	info, err := os.Stat(wavDir)
	if err != nil || !info.IsDir() {
		log.Fatalf("Invalid WAV directory path provided: %s", wavDir)
	}

	jsonDir = filepath.Join(filepath.Dir(wavDir), "json")
	m4aDir = filepath.Join(filepath.Dir(wavDir), "m4a")

	fmt.Printf("Source WAV directory: %s\n", wavDir)
	fmt.Printf("Metadata JSON directory: %s\n", jsonDir)
	fmt.Printf("M4A Cache directory: %s\n", m4aDir)

	if err := os.MkdirAll(jsonDir, 0755); err != nil {
		log.Fatalf("Failed to create %s directory: %v", jsonDir, err)
	}
	if err := os.MkdirAll(m4aDir, 0755); err != nil {
		log.Fatalf("Failed to create %s directory: %v", m4aDir, err)
	}

	fmt.Println("Initializing audio data...")
	if err := initAudioData(); err != nil {
		log.Fatalf("Error initializing audio data: %v", err)
	}
	fmt.Println("Audio data initialization complete.")

	// --- 根据 -gen 参数决定执行流程 ---
	if *genFlag {
		// 直接生成并退出
		fmt.Println("Generation-only mode activated.")
		if err := runGenerationLogic(); err != nil {
			log.Fatalf("Failed to generate static site: %v", err)
		}
		fmt.Println("Static site generated successfully in 'dist' directory.")
	} else {
		// 启动 web 服务器
		startWebServer()
	}
}

func startWebServer() {
	http.HandleFunc("/", adminHandler)
	http.HandleFunc("/about", aboutHandler)
	http.HandleFunc("/edit-about", editAboutHandler)
	http.HandleFunc("/save-about", saveAboutHandler)
	http.HandleFunc("/edit", editHandler)
	http.HandleFunc("/save", saveHandler)
	http.HandleFunc("/edit-folder", editFolderHandler)
	http.HandleFunc("/save-folder", saveFolderHandler)
	http.HandleFunc("/generate", generateStaticSiteHandler)
	http.Handle("/site/", http.StripPrefix("/site/", http.FileServer(http.Dir(distDir))))
	fmt.Println("Admin server starting on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

// ... (辅助函数) ...
func parseTimeFromFilename(filename string) (time.Time, bool) {
	match := timeRegex.FindString(filename)
	if match == "" {
		return time.Time{}, false
	}
	var parsedTime time.Time
	var err error
	if len(match) == 13 {
		parsedTime, err = time.Parse("060102_150405", match)
	} else if len(match) == 15 {
		parsedTime, err = time.Parse("20060102_150405", match)
	}
	if err == nil {
		return parsedTime, true
	}
	return time.Time{}, false
}

func isSpecialJsonFile(filename string) bool {
	for _, f := range specialJsonFiles {
		if f == filename {
			return true
		}
	}
	return false
}

func initAudioData() error {
	wavFilesFound := make(map[string]bool)
	walkErr := filepath.Walk(wavDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(strings.ToLower(info.Name()), ".wav") {
			relPath, err := filepath.Rel(wavDir, path)
			if err != nil {
				return fmt.Errorf("failed to get relative path for %s: %w", path, err)
			}
			wavFilesFound[relPath] = true
			jsonFileRelPath := strings.TrimSuffix(relPath, filepath.Ext(relPath)) + ".json"
			jsonFilePath := filepath.Join(jsonDir, jsonFileRelPath)
			if err := os.MkdirAll(filepath.Dir(jsonFilePath), 0755); err != nil {
				return fmt.Errorf("failed to create directory for json file %s: %w", jsonFilePath, err)
			}
			var metadata AudioMetadata
			newFile := false
			recordDateFromFilename, okFromFilename := parseTimeFromFilename(info.Name())
			recordDateToUse := getCreationTime(info)
			if okFromFilename {
				recordDateToUse = recordDateFromFilename
			}
			jsonContent, err := os.ReadFile(jsonFilePath)
			if err != nil {
				if os.IsNotExist(err) {
					newFile = true
					metadata = AudioMetadata{
						SourceFilename:   relPath,
						Title:            strings.TrimSuffix(info.Name(), filepath.Ext(info.Name())),
						RecordDate:       recordDateToUse,
						SourceFileSizeMB: float64(info.Size()) / (1024 * 1024),
						TechInfo: struct {
							SampleRate   int  `json:"sample_rate"`
							BitDepth     int  `json:"bit_depth"`
							Channels     int  `json:"channels"`
							IsCompressed bool `json:"is_compressed"`
						}{IsCompressed: false},
					}
				} else {
					return fmt.Errorf("failed to read json file %s: %w", jsonFilePath, err)
				}
			} else {
				if err := json.Unmarshal(jsonContent, &metadata); err != nil {
					newFile = true
					log.Printf("Failed to unmarshal json %s: %v. Re-creating metadata.", jsonFilePath, err)
					metadata = AudioMetadata{
						SourceFilename:   relPath,
						Title:            strings.TrimSuffix(info.Name(), filepath.Ext(info.Name())),
						RecordDate:       recordDateToUse,
						SourceFileSizeMB: float64(info.Size()) / (1024 * 1024),
						TechInfo: struct {
							SampleRate   int  `json:"sample_rate"`
							BitDepth     int  `json:"bit_depth"`
							Channels     int  `json:"channels"`
							IsCompressed bool `json:"is_compressed"`
						}{IsCompressed: false},
					}
				} else {
					metadata.RecordDate = recordDateToUse
					metadata.SourceFileSizeMB = float64(info.Size()) / (1024 * 1024)
				}
			}
			if newFile || metadata.TechInfo.SampleRate == 0 {
				duration, sampleRate, bitDepth, channels, err := getAudioTechInfo(path)
				if err != nil {
					log.Printf("Warning: Failed to get tech info for %s: %v", info.Name(), err)
				} else {
					metadata.DurationSeconds, metadata.TechInfo.SampleRate, metadata.TechInfo.BitDepth, metadata.TechInfo.Channels = duration, sampleRate, bitDepth, channels
				}
			}
			aacRelPath := strings.TrimSuffix(relPath, filepath.Ext(relPath)) + ".m4a"
			metadata.CompressedAudioPath = filepath.ToSlash(filepath.Join("assets", "audio", aacRelPath))
			metadata.Title, metadata.Description, metadata.Location = strings.ReplaceAll(metadata.Title, "\r", ""), strings.ReplaceAll(metadata.Description, "\r", ""), strings.ReplaceAll(metadata.Location, "\r", "")
			updatedJsonContent, err := json.MarshalIndent(metadata, "", "  ")
			if err != nil {
				return fmt.Errorf("failed to marshal json for %s: %w", info.Name(), err)
			}
			if err := os.WriteFile(jsonFilePath, updatedJsonContent, 0644); err != nil {
				return fmt.Errorf("failed to write json file %s: %w", jsonFilePath, err)
			}
		}
		return nil
	})
	if walkErr != nil {
		return fmt.Errorf("error walking through wav directory: %w", walkErr)
	}
	walkErr = filepath.Walk(jsonDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(info.Name(), ".json") {
			// Do not delete special json files
			if isSpecialJsonFile(info.Name()) {
				return nil
			}

			jsonRelPath, err := filepath.Rel(jsonDir, path)
			if err != nil {
				return err
			}
			wavRelPath := strings.TrimSuffix(jsonRelPath, ".json") + ".wav"
			hasWav := false
			if _, found := wavFilesFound[wavRelPath]; found {
				hasWav = true
			} else {
				wavRelPathUpper := strings.TrimSuffix(jsonRelPath, ".json") + ".WAV"
				if _, foundUpper := wavFilesFound[wavRelPathUpper]; foundUpper {
					hasWav = true
				}
			}

			hasM4aCache := m4aCacheExists(jsonRelPath)

			// If neither WAV nor M4A cache exists, then it's a true orphan
			if !hasWav && !hasM4aCache {
				log.Printf("Orphan json file found, deleting: %s", path)
				if err := os.Remove(path); err != nil {
					log.Printf("Failed to delete orphan json %s: %v", path, err)
				}
			}
		}
		return nil
	})
	return walkErr
}

func getAudioTechInfo(audioPath string) (duration float64, sampleRate, bitDepth, channels int, err error) {
	type FFProbeStream struct {
		SampleRate    string `json:"sample_rate"`
		Channels      int    `json:"channels"`
		BitsPerSample int    `json:"bits_per_sample"`
	}
	type FFProbeFormat struct {
		DurationStr string `json:"duration"`
	}
	type FFProbeOutput struct {
		Streams []FFProbeStream `json:"streams"`
		Format  FFProbeFormat   `json:"format"`
	}
	stdout, stderr, cmdErr := runCommand("ffprobe", "-v", "quiet", "-print_format", "json", "-show_format", "-show_streams", audioPath)
	if cmdErr != nil {
		return 0, 0, 0, 0, fmt.Errorf("ffprobe command failed: %v, stderr: %s", cmdErr, stderr)
	}
	var ffprobeData FFProbeOutput
	if err := json.Unmarshal([]byte(stdout), &ffprobeData); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("failed to unmarshal ffprobe json output: %w, output: %s", err, stdout)
	}
	if ffprobeData.Format.DurationStr != "" {
		duration, _ = strconv.ParseFloat(ffprobeData.Format.DurationStr, 64)
	}
	for _, stream := range ffprobeData.Streams {
		if stream.SampleRate != "" {
			sampleRate, _ = strconv.Atoi(stream.SampleRate)
			return duration, sampleRate, stream.BitsPerSample, stream.Channels, nil
		}
	}
	return duration, 0, 0, 0, fmt.Errorf("no valid audio stream found in %s", audioPath)
}

func m4aCacheExists(jsonRelPath string) bool {
	m4aCacheRelPath := strings.TrimSuffix(jsonRelPath, ".json") + ".m4a"
	m4aCachePath := filepath.Join(m4aDir, m4aCacheRelPath)
	_, err := os.Stat(m4aCachePath)
	return err == nil
}

func transcodeToAac(inputPath, outputPath string) error {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return fmt.Errorf("failed to create output directory %s: %w", filepath.Dir(outputPath), err)
	}
	_, stderr, err := runCommand("ffmpeg", "-i", inputPath, "-y", "-vn", "-c:a", "aac", "-vbr", "4", outputPath)
	if err != nil {
		return fmt.Errorf("ffmpeg transcode failed: %v, stderr: %s", err, stderr)
	}
	return nil
}

// copyM4aToDist 负责将 data/m4a 中的缓存文件复制到 dist/assets/audio
func copyM4aToDist(srcM4aPath, relPath string) (string, error) {
	dstAacRelPath := filepath.Join("assets", "audio", relPath)
	dstAacPath := filepath.Join(distDir, dstAacRelPath)
	if err := os.MkdirAll(filepath.Dir(dstAacPath), 0755); err != nil {
		return "", fmt.Errorf("failed to create output directory %s: %w", filepath.Dir(dstAacPath), err)
	}
	if err := copyFile(srcM4aPath, dstAacPath); err != nil {
		return "", fmt.Errorf("failed to copy %s to %s: %w", srcM4aPath, dstAacPath, err)
	}
	return filepath.ToSlash(dstAacRelPath), nil
}

func loadAllMetadataGroupedByFolder() (map[string][]AudioMetadata, error) {
	groupedMetadata := make(map[string][]AudioMetadata)
	walkErr := filepath.Walk(jsonDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(info.Name(), ".json") {
			// Do not process special json files as audio metadata
			if isSpecialJsonFile(info.Name()) {
				return nil
			}

			metadata, err := loadAudioMetadata(path)
			if err != nil {
				log.Printf("Warning: %v", err)
				return nil
			}

			dir := filepath.Dir(metadata.SourceFilename)
			if dir == "." {
				dir = "/"
			}
			groupedMetadata[dir] = append(groupedMetadata[dir], metadata)
		}
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("error walking through json directory: %w", walkErr)
	}
	return groupedMetadata, nil
}

func loadAudioMetadata(path string) (AudioMetadata, error) {
	var metadata AudioMetadata
	jsonContent, err := os.ReadFile(path)
	if err != nil {
		return metadata, fmt.Errorf("failed to read json file %s: %w", path, err)
	}
	if err := json.Unmarshal(jsonContent, &metadata); err != nil {
		return metadata, fmt.Errorf("failed to unmarshal json for %s: %w", path, err)
	}
	return metadata, nil
}

func getMetadataBySourceFilename(filename string) (AudioMetadata, error) {
	jsonFileRelPath := strings.TrimSuffix(filename, filepath.Ext(filename)) + ".json"
	jsonFilePath := filepath.Join(jsonDir, jsonFileRelPath)
	return loadAudioMetadata(jsonFilePath)
}

func loadSettings() (Settings, error) {
	var settings Settings
	jsonPath := filepath.Join(jsonDir, "settings.json")
	jsonContent, err := os.ReadFile(jsonPath)
	if err != nil {
		if os.IsNotExist(err) {
			return Settings{Domain: "https://your-domain.com"}, nil
		}
		return settings, fmt.Errorf("failed to read settings.json: %w", err)
	}
	if err := json.Unmarshal(jsonContent, &settings); err != nil {
		return settings, fmt.Errorf("failed to unmarshal settings.json: %w", err)
	}
	return settings, nil
}

func loadAboutContent() (AboutContent, error) {
	var content AboutContent
	jsonPath := filepath.Join(jsonDir, "about.json")
	jsonContent, err := os.ReadFile(jsonPath)
	if err != nil {
		if os.IsNotExist(err) {
			// 如果文件不存在，返回一个带默认值的新结构体
			return AboutContent{
				Content: "请在这里写下关于您自己和这个网站的故事...",
				Email:   "your-email@example.com",
			}, nil
		}
		return content, fmt.Errorf("failed to read about.json: %w", err)
	}
	if err := json.Unmarshal(jsonContent, &content); err != nil {
		return content, fmt.Errorf("failed to unmarshal about.json: %w", err)
	}
	return content, nil
}

func aboutHandler(w http.ResponseWriter, r *http.Request) {
	content, err := loadAboutContent()
	if err != nil {
		log.Printf("Error loading about page content: %v", err)
		http.Error(w, "Internal Server Error", 500)
		return
	}
	tmpl, err := template.ParseFS(templateFS, "templates/about.html")
	if err != nil {
		log.Printf("Error parsing template about.html: %v", err)
		http.Error(w, "Internal Server Error", 500)
		return
	}
	data := AboutPageData{
		AboutContent: content,
		IsAdmin:      true, // This is the admin/live view
	}
	if err := tmpl.Execute(w, data); err != nil {
		log.Printf("Error executing about.html template: %v", err)
		http.Error(w, "Internal Server Error", 500)
	}
}

func editAboutHandler(w http.ResponseWriter, r *http.Request) {
	content, err := loadAboutContent()
	if err != nil {
		log.Printf("Error loading about page content for editing: %v", err)
		http.Error(w, "Internal Server Error", 500)
		return
	}
	tmpl, err := template.ParseFS(templateFS, "templates/edit_about.html")
	if err != nil {
		log.Printf("Error parsing template edit_about.html: %v", err)
		http.Error(w, "Internal Server Error", 500)
		return
	}
	if err := tmpl.Execute(w, content); err != nil {
		log.Printf("Error executing edit_about.html template: %v", err)
		http.Error(w, "Internal Server Error", 500)
	}
}

func saveAboutHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Only POST requests are allowed", http.StatusMethodNotAllowed)
		return
	}
	content := AboutContent{
		Content: strings.ReplaceAll(r.FormValue("content"), "\r", ""),
		Email:   strings.ReplaceAll(r.FormValue("email"), "\r", ""),
	}
	jsonPath := filepath.Join(jsonDir, "about.json")
	updatedJsonContent, err := json.MarshalIndent(content, "", "  ")
	if err != nil {
		log.Printf("Failed to marshal about content: %v", err)
		http.Error(w, "Failed to save content", 500)
		return
	}
	if err := os.WriteFile(jsonPath, updatedJsonContent, 0644); err != nil {
		log.Printf("Failed to write about.json file: %v", err)
		http.Error(w, "Failed to save content", 500)
	}
	http.Redirect(w, r, "/about", http.StatusSeeOther)
}

func adminHandler(w http.ResponseWriter, r *http.Request) {
	tmpl, err := template.New("admin.html").Funcs(template.FuncMap{"Base": filepath.Base, "formatDuration": formatDuration}).ParseFS(templateFS, "templates/admin.html")
	if err != nil {
		log.Printf("Error parsing template admin.html: %v", err)
		http.Error(w, "Internal Server Error", 500)
		return
	}
	groupedMetadata, err := loadAllMetadataGroupedByFolder()
	if err != nil {
		log.Printf("Error loading all metadata: %v", err)
		http.Error(w, "Internal Server Error", 500)
		return
	}
	if err := tmpl.Execute(w, groupedMetadata); err != nil {
		log.Printf("Error executing template: %v", err)
		http.Error(w, "Internal Server Error", 500)
	}
}

func editHandler(w http.ResponseWriter, r *http.Request) {
	filename := r.URL.Query().Get("filename")
	if filename == "" {
		http.Error(w, "Filename parameter is missing", 400)
		return
	}
	metadata, err := getMetadataBySourceFilename(filename)
	if err != nil {
		log.Printf("Error getting metadata for %s: %v", filename, err)
		http.Error(w, "Audio not found", 404)
		return
	}
	tmpl, err := template.New("edit.html").Funcs(template.FuncMap{"Base": filepath.Base}).ParseFS(templateFS, "templates/edit.html")
	if err != nil {
		log.Printf("Error parsing template edit.html: %v", err)
		http.Error(w, "Internal Server Error", 500)
		return
	}
	if err := tmpl.Execute(w, metadata); err != nil {
		log.Printf("Error executing template: %v", err)
		http.Error(w, "Internal Server Error", 500)
	}
}

func saveHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Only POST requests are allowed", http.StatusMethodNotAllowed)
		return
	}
	sourceFilename := r.FormValue("source_filename")
	if sourceFilename == "" {
		http.Error(w, "Source filename is missing", 400)
		return
	}
	metadata, err := getMetadataBySourceFilename(sourceFilename)
	if err != nil {
		log.Printf("Error getting metadata for %s during save: %v", sourceFilename, err)
		http.Error(w, "Audio not found", 404)
		return
	}
	metadata.Title = strings.ReplaceAll(r.FormValue("title"), "\r", "")
	metadata.Description = strings.ReplaceAll(r.FormValue("description"), "\r", "")
	metadata.Location = strings.ReplaceAll(r.FormValue("location"), "\r", "")
	if recordDateStr := r.FormValue("record_date"); recordDateStr != "" {
		if parsedTime, err := time.Parse("2006-01-02 15:04:05", recordDateStr); err != nil {
			log.Printf("Warning: Failed to parse record_date '%s': %v", recordDateStr, err)
		} else {
			metadata.RecordDate = parsedTime
		}
	}
	jsonFileRelPath := strings.TrimSuffix(sourceFilename, filepath.Ext(sourceFilename)) + ".json"
	jsonFilePath := filepath.Join(jsonDir, jsonFileRelPath)
	updatedJsonContent, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		log.Printf("Failed to marshal json for %s: %v", sourceFilename, err)
		http.Error(w, "Failed to save metadata", 500)
		return
	}
	if err := os.WriteFile(jsonFilePath, updatedJsonContent, 0644); err != nil {
		log.Printf("Failed to write json file %s: %v", jsonFilePath, err)
		http.Error(w, "Failed to save metadata", 500)
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func editFolderHandler(w http.ResponseWriter, r *http.Request) {
	folderPath := r.URL.Query().Get("path")
	if folderPath == "" {
		http.Error(w, "Folder path parameter is missing", 400)
		return
	}
	allMetadata, err := loadAllMetadataGroupedByFolder()
	if err != nil {
		http.Error(w, "Failed to load metadata", 500)
		return
	}
	filesInFolder, ok := allMetadata[folderPath]
	if !ok || len(filesInFolder) == 0 {
		http.Error(w, "Folder not found or is empty", 404)
		return
	}
	currentLocation := filesInFolder[0].Location
	tmpl, err := template.ParseFS(templateFS, "templates/edit_folder.html")
	if err != nil {
		http.Error(w, "Internal Server Error", 500)
		return
	}
	data := struct {
		Path            string
		CurrentLocation string
	}{Path: folderPath, CurrentLocation: currentLocation}
	if err := tmpl.Execute(w, data); err != nil {
		http.Error(w, "Internal Server Error", 500)
	}
}

func saveFolderHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Only POST requests are allowed", http.StatusMethodNotAllowed)
		return
	}
	folderPath := r.FormValue("path")
	newLocation := strings.ReplaceAll(r.FormValue("location"), "\r", "")
	if folderPath == "" {
		http.Error(w, "Folder path is missing", 400)
		return
	}
	walkErr := filepath.Walk(jsonDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(info.Name(), ".json") {
			if isSpecialJsonFile(info.Name()) {
				return nil
			}
			metadata, err := loadAudioMetadata(path)
			if err != nil {
				log.Printf("Warning: could not load metadata for %s during folder save: %v", path, err)
				return nil // Continue to next file
			}
			if filepath.Dir(metadata.SourceFilename) == folderPath {
				metadata.Location = newLocation
				updatedJson, err := json.MarshalIndent(metadata, "", "  ")
				if err != nil {
					log.Printf("Failed to marshal json for %s: %v", metadata.SourceFilename, err)
					return nil
				}
				if err := os.WriteFile(path, updatedJson, 0644); err != nil {
					log.Printf("Failed to write updated json for %s: %v", metadata.SourceFilename, err)
				}
			}
		}
		return nil
	})
	if walkErr != nil {
		http.Error(w, "Error processing folder update", 500)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open source file %s: %w", src, err)
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fmt.Errorf("failed to create destination directory %s: %w", filepath.Dir(dst), err)
	}
	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("failed to create destination file %s: %w", dst, err)
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	if err != nil {
		return fmt.Errorf("failed to copy file from %s to %s: %w", src, dst, err)
	}
	return out.Close()
}

func add(a, b int) int { return a + b }

// runGenerationLogic 包含了生成静态网站的核心逻辑
func runGenerationLogic() error {
	log.Println("Generating static site...")

	// Completely remove and recreate dist directory as m4a cache is now persistent
	if err := os.RemoveAll(distDir); err != nil {
		return fmt.Errorf("failed to clean dist directory: %w", err)
	}
	if err := os.MkdirAll(assetsAudioDir, 0755); err != nil {
		return fmt.Errorf("failed to create assets audio directory: %w", err)
	}

	groupedMetadata, err := loadAllMetadataGroupedByFolder()
	if err != nil {
		return fmt.Errorf("failed to load audio metadata: %w", err)
	}

	var flatMetadata []AudioMetadata
	for _, files := range groupedMetadata {
		flatMetadata = append(flatMetadata, files...)
	}

	sort.Slice(flatMetadata, func(i, j int) bool {
		return flatMetadata[i].RecordDate.After(flatMetadata[j].RecordDate)
	})

	var processedMetadata []AudioMetadata // To store only valid, processed metadata

	for i := range flatMetadata {
		meta := &flatMetadata[i]
		originalJsonPath := filepath.Join(jsonDir, strings.TrimSuffix(meta.SourceFilename, filepath.Ext(meta.SourceFilename))+".json")

		srcWavPath := filepath.Join(wavDir, meta.SourceFilename)
		m4aCacheFileRelPath := strings.TrimSuffix(meta.SourceFilename, filepath.Ext(meta.SourceFilename)) + ".m4a"
		m4aCachePath := filepath.Join(m4aDir, m4aCacheFileRelPath)

		wavExists := false
		if _, err := os.Stat(srcWavPath); err == nil {
			wavExists = true
		}

		m4aCacheExists := false
		if _, err := os.Stat(m4aCachePath); err == nil {
			m4aCacheExists = true
		}

		var currentSourcePath string // The path to the M4A file in the cache

		if wavExists {
			// Scenario 1: WAV file exists - prefer WAV as source
			srcWavInfo, _ := os.Stat(srcWavPath) // Error already checked by wavExists
			m4aCacheInfo, m4aCacheErr := os.Stat(m4aCachePath)

			shouldTranscode := true
			if m4aCacheErr == nil && m4aCacheInfo.ModTime().After(srcWavInfo.ModTime()) {
				// M4A cache exists and is newer than WAV, no need to transcode
				// log.Printf("Using existing M4A cache for %s (newer than WAV).", meta.SourceFilename) // Simplified log
				shouldTranscode = false
			}

			if shouldTranscode {
				// Transcode WAV to M4A cache
				log.Printf("Transcoding %s to M4A cache...", meta.SourceFilename)
				if err := transcodeToAac(srcWavPath, m4aCachePath); err != nil {
					log.Printf("Error transcoding %s to M4A cache: %v. Skipping this audio.", meta.SourceFilename, err)
					// If transcoding fails, we can't process this audio
					continue
				}
			}
			currentSourcePath = m4aCachePath

		} else if m4aCacheExists {
			// Scenario 2: WAV does not exist, but M4A cache exists - use cached M4A
			log.Printf("WAV not found for %s. Using M4A from cache.", meta.SourceFilename)
			currentSourcePath = m4aCachePath

		} else {
			// Scenario 3: Neither WAV nor M4A cache exists - audio is truly lost
			log.Printf("Warning: WAV and M4A cache not found for %s. Deleting corresponding JSON: %s", meta.SourceFilename, originalJsonPath)
			if err := os.Remove(originalJsonPath); err != nil {
				log.Printf("Error deleting orphan JSON %s: %v", originalJsonPath, err)
			}
			continue // Skip this audio
		}

		// At this point, currentSourcePath points to a valid M4A in the cache
		// Now copy it to dist/assets/audio and update metadata
		relPath := m4aCacheFileRelPath // The relative path within assets/audio

		// Use the copyM4aToDist helper
		compressedAudioPath, err := copyM4aToDist(currentSourcePath, relPath)
		if err != nil {
			log.Printf("Error copying M4A cache %s to dist: %v. Skipping this audio.", currentSourcePath, err)
			continue
		}

		meta.CompressedAudioPath = compressedAudioPath // Relative path for HTML
		if aacFileInfo, err := os.Stat(currentSourcePath); err == nil {
			meta.CompressedFileSizeMB = float64(aacFileInfo.Size()) / (1024 * 1024)
		} else {
			log.Printf("Warning: Could not get file info for cached M4A %s: %v", currentSourcePath, err)
			meta.CompressedFileSizeMB = 0
		}

		processedMetadata = append(processedMetadata, *meta)
	}

	// Replace flatMetadata with processedMetadata
	flatMetadata = processedMetadata

	tmpl, err := template.New("index.html.tmpl").Funcs(template.FuncMap{"Base": filepath.Base, "formatDuration": formatDuration, "add": add}).ParseFS(templateFS, "templates/index.html.tmpl")
	if err != nil {
		return fmt.Errorf("failed to parse template index.html.tmpl: %w", err)
	}

	indexPath := filepath.Join(distDir, "index.html")
	f, err := os.Create(indexPath)
	if err != nil {
		return fmt.Errorf("failed to create index.html: %w", err)
	}
	defer f.Close()

	if err := tmpl.Execute(f, flatMetadata); err != nil {
		return fmt.Errorf("failed to execute template for index.html: %w", err)
	}
	log.Printf("Generated %s", indexPath)

	if err := copyFile("icon.svg", filepath.Join(distDir, "icon.svg")); err != nil {
		log.Printf("Warning: could not copy icon.svg: %v", err)
	}

	if err := filepath.Walk(staticDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, _ := filepath.Rel(staticDir, path)
		if relPath == "." {
			return nil
		}
		destPath := filepath.Join(distDir, relPath)
		if info.IsDir() {
			return os.MkdirAll(destPath, info.Mode())
		}
		return copyFile(path, destPath)
	}); err != nil {
		log.Printf("Warning: error copying static assets: %v", err)
	}

	// --- SEO File Generation ---
	log.Println("Generating SEO files...")
	settings, err := loadSettings()
	if err != nil {
		return fmt.Errorf("failed to load settings for static generation: %w", err)
	}

	// Generate about.html
	aboutContent, err := loadAboutContent()
	if err != nil {
		return fmt.Errorf("failed to load about content for static generation: %w", err)
	}
	aboutTmpl, err := template.ParseFS(templateFS, "templates/about.html")
	if err != nil {
		return fmt.Errorf("failed to parse about.html template for static generation: %w", err)
	}
	aboutPath := filepath.Join(distDir, "about.html")
	aboutFile, err := os.Create(aboutPath)
	if err != nil {
		return fmt.Errorf("failed to create about.html: %w", err)
	}
	defer aboutFile.Close()

	data := AboutPageData{
		AboutContent: aboutContent,
		IsAdmin:      false, // This is for the static, public site
	}
	if err := aboutTmpl.Execute(aboutFile, data); err != nil {
		return fmt.Errorf("failed to execute template for about.html: %w", err)
	}
	log.Printf("Generated %s", aboutPath)

	// Generate sitemap.xml
	sitemapPath := filepath.Join(distDir, "sitemap.xml")
	sitemapContent := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url>
    <loc>%s/</loc>
    <lastmod>%s</lastmod>
    <changefreq>daily</changefreq>
    <priority>1.0</priority>
  </url>
  <url>
    <loc>%s/about.html</loc>
    <lastmod>%s</lastmod>
    <changefreq>weekly</changefreq>
    <priority>0.8</priority>
  </url>
</urlset>`, settings.Domain, time.Now().Format("2006-01-02"), settings.Domain, time.Now().Format("2006-01-02"))

	if err := os.WriteFile(sitemapPath, []byte(sitemapContent), 0644); err != nil {
		return fmt.Errorf("failed to write sitemap.xml: %w", err)
	}
	log.Printf("Generated %s", sitemapPath)

	// Generate robots.txt
	robotsPath := filepath.Join(distDir, "robots.txt")
	robotsContent := fmt.Sprintf("User-agent: *\nAllow: /\nSitemap: %s/sitemap.xml", settings.Domain)

	if err := os.WriteFile(robotsPath, []byte(robotsContent), 0644); err != nil {
		return fmt.Errorf("failed to write robots.txt: %w", err)
	}
	log.Printf("Generated %s", robotsPath)

	return nil
}

func generateStaticSiteHandler(w http.ResponseWriter, r *http.Request) {
	if err := runGenerationLogic(); err != nil {
		log.Printf("Error during static site generation: %v", err)
		http.Error(w, "Failed to generate static site", 500)
		return
	}

	log.Println("Static site generation complete.")
	tmpl, err := template.ParseFS(templateFS, "templates/generate_success.html")
	if err != nil {
		log.Printf("Error parsing success template: %v", err)
		http.Error(w, "Internal Server Error", 500)
		return
	}
	if err := tmpl.Execute(w, nil); err != nil {
		log.Printf("Error executing success template: %v", err)
		http.Error(w, "Internal Server Error", 500)
	}
}
