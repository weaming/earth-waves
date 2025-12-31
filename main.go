package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

//go:embed templates/*
var templateFS embed.FS

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
	} `json:"tech_info"` // 恢复 TechInfo 字段
}

var (
	wavDir         string
	jsonDir        string
	distDir        = "dist"
	assetsAudioDir = "dist/assets/audio"
	staticDir      = "static" // 重新定义 staticDir
	timeRegex      = regexp.MustCompile(`(\d{8}_\d{6}|\d{6}_\d{6})`)
)

func main() {
	// --- 命令行参数处理 ---
	wavPathFlag := flag.String("wav", "", "Path to the directory containing WAV files (required)")
	flag.Parse()

	if *wavPathFlag == "" {
		fmt.Println("WAV directory path is required.")
		flag.Usage()
		os.Exit(1)
	}

	wavDir = *wavPathFlag
	info, err := os.Stat(wavDir)
	if err != nil || !info.IsDir() {
		log.Fatalf("Invalid WAV directory path: %s", wavDir)
	}

	// json 目录放在 wav 目录的同级
	jsonDir = filepath.Join(filepath.Dir(wavDir), "json")

	fmt.Printf("Source WAV directory: %s\n", wavDir)
	fmt.Printf("Metadata JSON directory: %s\n", jsonDir)

	// --- 程序启动 ---
	if err := os.MkdirAll(wavDir, 0755); err != nil {
		log.Fatalf("Failed to create %s directory: %v", wavDir, err)
	}
	if err := os.MkdirAll(jsonDir, 0755); err != nil {
		log.Fatalf("Failed to create %s directory: %v", jsonDir, err)
	}

	fmt.Println("Initializing audio data...")
	if err := initAudioData(); err != nil {
		log.Fatalf("Error initializing audio data: %v", err)
	}
	fmt.Println("Audio data initialization complete.")

	http.HandleFunc("/", adminHandler)
	http.HandleFunc("/edit", editHandler)
	http.HandleFunc("/save", saveHandler)
	http.HandleFunc("/edit-folder", editFolderHandler)
	http.HandleFunc("/save-folder", saveFolderHandler)
	http.HandleFunc("/generate", generateStaticSiteHandler)

	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir(staticDir))))
	http.Handle("/site/", http.StripPrefix("/site/", http.FileServer(http.Dir(distDir))))

	fmt.Println("Admin server starting on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

// parseTimeFromFilename 从文件名中解析时间
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
			jsonContent, err := ioutil.ReadFile(jsonFilePath)
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
			if err := ioutil.WriteFile(jsonFilePath, updatedJsonContent, 0644); err != nil {
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
			jsonRelPath, err := filepath.Rel(jsonDir, path)
			if err != nil {
				return err
			}
			wavRelPath := strings.TrimSuffix(jsonRelPath, ".json") + ".wav"
			if _, found := wavFilesFound[wavRelPath]; !found {
				wavRelPathUpper := strings.TrimSuffix(jsonRelPath, ".json") + ".WAV"
				if _, foundUpper := wavFilesFound[wavRelPathUpper]; !foundUpper {
					log.Printf("Orphan json file found, deleting: %s", path)
					if err := os.Remove(path); err != nil {
						log.Printf("Failed to delete orphan json %s: %v", path, err)
					}
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

func transcodeToAac(inputPath, outputPath string) error {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return fmt.Errorf("failed to create output directory %s: %w", filepath.Dir(outputPath), err)
	}
	_, stderr, err := runCommand("ffmpeg", "-i", inputPath, "-y", "-vn", "-c:a", "aac_at", "-vbr", "4", "-movflags", "+faststart", outputPath)
	if err != nil {
		return fmt.Errorf("ffmpeg transcode failed: %v, stderr: %s", err, stderr)
	}
	log.Printf("Successfully transcoded %s to %s", inputPath, outputPath)
	return nil
}

func loadAllMetadataGroupedByFolder() (map[string][]AudioMetadata, error) {
	groupedMetadata := make(map[string][]AudioMetadata)
	walkErr := filepath.Walk(jsonDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(info.Name(), ".json") {
			jsonContent, err := ioutil.ReadFile(path)
			if err != nil {
				log.Printf("Warning: Failed to read json file %s: %v", path, err)
				return nil
			}
			var metadata AudioMetadata
			if err := json.Unmarshal(jsonContent, &metadata); err != nil {
				log.Printf("Warning: Failed to unmarshal json for %s: %v", path, err)
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

func getMetadataBySourceFilename(filename string) (AudioMetadata, error) {
	jsonFileRelPath := strings.TrimSuffix(filename, filepath.Ext(filename)) + ".json"
	jsonFilePath := filepath.Join(jsonDir, jsonFileRelPath)
	jsonContent, err := ioutil.ReadFile(jsonFilePath)
	if err != nil {
		return AudioMetadata{}, fmt.Errorf("failed to read json file %s: %w", jsonFilePath, err)
	}
	var metadata AudioMetadata
	if err := json.Unmarshal(jsonContent, &metadata); err != nil {
		return AudioMetadata{}, fmt.Errorf("failed to unmarshal json for %s: %w", filename, err)
	}
	return metadata, nil
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
		http.Error(w, "Only POST requests are allowed", 405)
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
	if err := ioutil.WriteFile(jsonFilePath, updatedJsonContent, 0644); err != nil {
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
		http.Error(w, "Only POST requests are allowed", 405)
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
			jsonContent, err := ioutil.ReadFile(path)
			if err != nil {
				return nil
			}
			var metadata AudioMetadata
			if err := json.Unmarshal(jsonContent, &metadata); err != nil {
				return nil
			}
			if filepath.Dir(metadata.SourceFilename) == folderPath {
				metadata.Location = newLocation
				updatedJson, err := json.MarshalIndent(metadata, "", "  ")
				if err != nil {
					log.Printf("Failed to marshal json for %s: %v", metadata.SourceFilename, err)
					return nil
				}
				if err := ioutil.WriteFile(path, updatedJson, 0644); err != nil {
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

func generateStaticSiteHandler(w http.ResponseWriter, r *http.Request) {
	log.Println("Generating static site...")
	if err := os.RemoveAll(distDir); err != nil {
		log.Printf("Error cleaning dist directory: %v", err)
		http.Error(w, "Failed to clean dist directory", 500)
		return
	}
	if err := os.MkdirAll(assetsAudioDir, 0755); err != nil {
		log.Printf("Failed to create %s directory: %v", assetsAudioDir, err)
		http.Error(w, "Failed to create assets audio directory", 500)
		return
	}

	groupedMetadata, err := loadAllMetadataGroupedByFolder()
	if err != nil {
		log.Printf("Error loading all metadata for static site generation: %v", err)
		http.Error(w, "Failed to load audio metadata", 500)
		return
	}

	var flatMetadata []AudioMetadata
	for _, files := range groupedMetadata {
		flatMetadata = append(flatMetadata, files...)
	}

	for i := range flatMetadata {
		meta := &flatMetadata[i]
		srcWavPath := filepath.Join(wavDir, meta.SourceFilename)
		dstAacPath := filepath.Join(distDir, meta.CompressedAudioPath)
		if err := transcodeToAac(srcWavPath, dstAacPath); err != nil {
			log.Printf("Error transcoding %s to %s: %v", srcWavPath, dstAacPath, err)
			meta.CompressedFileSizeMB = 0
		} else {
			if aacFileInfo, err := os.Stat(dstAacPath); err != nil {
				log.Printf("Error getting info for transcoded AAC %s: %v", dstAacPath, err)
				meta.CompressedFileSizeMB = 0
			} else {
				meta.CompressedFileSizeMB = float64(aacFileInfo.Size()) / (1024 * 1024)
			}
		}
	}
	tmpl, err := template.New("index.html.tmpl").Funcs(template.FuncMap{"Base": filepath.Base, "formatDuration": formatDuration, "add": add}).ParseFS(templateFS, "templates/index.html.tmpl")
	if err != nil {
		log.Printf("Error parsing template index.html.tmpl for static site: %v", err)
		http.Error(w, "Internal Server Error", 500)
		return
	}
	indexPath := filepath.Join(distDir, "index.html")
	f, err := os.Create(indexPath)
	if err != nil {
		log.Printf("Error creating index.html: %v", err)
		http.Error(w, "Failed to create index.html", 500)
		return
	}
	defer f.Close()
	if err := tmpl.Execute(f, flatMetadata); err != nil {
		log.Printf("Error executing template for index.html: %v", err)
		http.Error(w, "Failed to generate index.html", 500)
		return
	}
	log.Printf("Generated %s", indexPath)
	if err := filepath.Walk(staticDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, _ := filepath.Rel(staticDir, path)
		destPath := filepath.Join(distDir, relPath)
		if info.IsDir() {
			return os.MkdirAll(destPath, info.Mode())
		}
		return copyFile(path, destPath)
	}); err != nil {
		log.Printf("Error copying static assets: %v", err)
	}
	log.Println("Static site generation complete.")
	tmpl, err = template.ParseFS(templateFS, "templates/generate_success.html")
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
