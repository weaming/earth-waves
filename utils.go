package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	wavDir         string
	jsonDir        string
	m4aDir         string
	distDir        = "dist"
	assetsAudioDir = "dist/assets/audio"
	staticDir      = "static"
	timeRegex      = regexp.MustCompile(`(\d{8}_\d{6}|\d{6}_\d{6})`)
)

// specialJsonFiles 列出了所有非音频元数据的特殊 JSON 文件，在处理时需要跳过
var specialJsonFiles = []string{"about.json", "settings.json"}

func getUserTimeLocation() *time.Location {
	tz, ok := os.LookupEnv("TZ")
	if ok {
		loc, err := time.LoadLocation(tz)
		if err == nil {
			log.Printf("Using user-defined timezone from TZ environment variable: %s", tz)
			return loc
		}
		log.Printf("Warning: Could not load timezone '%s' from TZ environment variable: %v. Falling back to Asia/Hong_Kong.", tz, err)
	}

	loc, err := time.LoadLocation("Asia/Hong_Kong")
	if err != nil {
		log.Printf("Warning: Could not load Asia/Hong_Kong timezone: %v. Falling back to UTC.", err)
		return time.UTC
	}
	log.Println("Using default timezone: Asia/Hong_Kong")
	return loc
}

// runCommand 运行一个 shell 命令并返回其标准输出和错误
func runCommand(name string, arg ...string) (string, string, error) {
	cmd := exec.Command(name, arg...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return stdout.String(), stderr.String(), fmt.Errorf("command failed: %s %v, stdout: %s, stderr: %s, error: %w", name, arg, stdout.String(), stderr.String(), err)
	}
	return stdout.String(), stderr.String(), nil
}

// formatDuration 将秒数格式化为 HH:MM:SS 或 MM:SS
func formatDuration(seconds float64) string {
	d := time.Duration(seconds) * time.Second
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second

	if h > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

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
			jsonContent, err := os.ReadFile(jsonFilePath)
			if err != nil {
				if os.IsNotExist(err) {
					newFile = true
					// Get creation time for the new file
					recordDate := GetBirthTime(info)
					// Try to parse time from filename, and if successful, use it instead
					if t, ok := parseTimeFromFilename(info.Name()); ok {
						recordDate = t
					}
					metadata = AudioMetadata{
						SourceFilename:   relPath,
						Title:            strings.TrimSuffix(info.Name(), filepath.Ext(info.Name())),
						RecordDate:       recordDate, // Set initial record date
						SourceFileSizeMB: float64(info.Size()) / (1024 * 1024),
						TechInfo:         AudioMetadata{}.TechInfo,
					}
				} else {
					return fmt.Errorf("failed to read json file %s: %w", jsonFilePath, err)
				}
			} else {
				if err := json.Unmarshal(jsonContent, &metadata); err != nil {
					newFile = true
					log.Printf("Failed to unmarshal json %s: %v. Re-creating metadata.", jsonFilePath, err)
					recordDate := GetBirthTime(info)
					if t, ok := parseTimeFromFilename(info.Name()); ok {
						recordDate = t
					}
					metadata = AudioMetadata{
						SourceFilename:   relPath,
						Title:            strings.TrimSuffix(info.Name(), filepath.Ext(info.Name())),
						RecordDate:       recordDate,
						SourceFileSizeMB: float64(info.Size()) / (1024 * 1024),
						TechInfo:         AudioMetadata{}.TechInfo,
					}
				} else {
					// Update size, as it might have changed
					metadata.SourceFileSizeMB = float64(info.Size()) / (1024 * 1024)
				}
			}

			// Get tech info only if it's a new file or seems to be missing
			if newFile || metadata.TechInfo.SampleRate == 0 {
				duration, sampleRate, bitDepth, channels, err := getAudioTechInfo(path)
				if err != nil {
					log.Printf("Warning: Failed to get tech info for %s: %v", info.Name(), err)
				} else {
					metadata.DurationSeconds = duration
					metadata.TechInfo.SampleRate = sampleRate
					metadata.TechInfo.BitDepth = bitDepth
					metadata.TechInfo.Channels = channels
				}
			}

			// Always ensure these fields are correct
			aacRelPath := strings.TrimSuffix(relPath, filepath.Ext(relPath)) + ".m4a"
			metadata.CompressedAudioPath = filepath.ToSlash(filepath.Join("assets", "audio", aacRelPath))
			metadata.SourceFilename = relPath // Ensure source filename is up-to-date
			metadata.Title, metadata.Description, metadata.Location = strings.ReplaceAll(metadata.Title, "\r", ""), strings.ReplaceAll(metadata.Description, "\r", ""), strings.ReplaceAll(metadata.Location, "\r", "")

			// Write back the JSON file
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

func syncAudioData() error {
	audioDirs := []string{wavDir, m4aDir}
	for _, dir := range audioDirs {
		walkErr := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.IsDir() && (strings.HasSuffix(strings.ToLower(info.Name()), ".wav") || strings.HasSuffix(strings.ToLower(info.Name()), ".m4a")) {
				// Determine the base directory (wavDir or m4aDir) to correctly calculate the relative path
				var baseDir string
				if strings.HasPrefix(path, wavDir) {
					baseDir = wavDir
				} else {
					baseDir = m4aDir
				}

				relPath, err := filepath.Rel(baseDir, path)
				if err != nil {
					return fmt.Errorf("failed to get relative path for %s: %w", path, err)
				}

				jsonFileRelPath := strings.TrimSuffix(relPath, filepath.Ext(relPath)) + ".json"
				jsonFilePath := filepath.Join(jsonDir, jsonFileRelPath)

				if _, err := os.Stat(jsonFilePath); os.IsNotExist(err) {
					// JSON file doesn't exist, initAudioData will handle creating it for WAVs.
					// For M4A-only cases, we might need a separate creation logic if desired.
					return nil
				}

				metadata, err := loadAudioMetadata(jsonFilePath)
				if err != nil {
					log.Printf("Warning: could not load metadata for %s to sync time: %v", jsonFilePath, err)
					return nil // Continue to next file
				}

				birthTime := GetBirthTime(info)

				// Only update if the time is different to avoid unnecessary writes
				if !metadata.RecordDate.Equal(birthTime) {
					log.Printf("Syncing time for %s. Old: %s, New: %s", relPath, metadata.RecordDate.Format(time.RFC3339), birthTime.Format(time.RFC3339))
					metadata.RecordDate = birthTime
					updatedJsonContent, err := json.MarshalIndent(metadata, "", "  ")
					if err != nil {
						log.Printf("Failed to marshal json for %s during time sync: %v", info.Name(), err)
						return nil // Continue
					}
					if err := os.WriteFile(jsonFilePath, updatedJsonContent, 0644); err != nil {
						log.Printf("Failed to write json file %s during time sync: %v", jsonFilePath, err)
					}
				}
			}
			return nil
		})
		if walkErr != nil {
			return fmt.Errorf("error walking through %s directory for sync: %w", dir, walkErr)
		}
	}
	return nil
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
	// Sort folders by name
	sortedGroupedMetadata := make(map[string][]AudioMetadata)
	var keys []string
	for k := range groupedMetadata {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sortedGroupedMetadata[k] = groupedMetadata[k]
	}

	return sortedGroupedMetadata, nil
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
				},
				nil
		}
		return content, fmt.Errorf("failed to read about.json: %w", err)
	}
	if err := json.Unmarshal(jsonContent, &content); err != nil {
		return content, fmt.Errorf("failed to unmarshal about.json: %w", err)
	}
	return content, nil
}

// isDirEmpty checks if a directory is empty.
func isDirEmpty(name string) (bool, error) {
	f, err := os.Open(name)
	if err != nil {
		return false, err
	}
	defer f.Close()

	// Read just one entry from the directory.
	// If it's not EOF, the directory is not empty.
	_, err = f.Readdir(1)
	if err == io.EOF {
		return true, nil
	}
	return false, err
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

func updateAssociatedFileTimestamps(sourceFilename string, t time.Time) {
	ext := filepath.Ext(sourceFilename)
	baseFilename := strings.TrimSuffix(sourceFilename, ext)

	wavPath := filepath.Join(wavDir, sourceFilename)
	jsonPath := filepath.Join(jsonDir, baseFilename+".json")
	m4aPath := filepath.Join(m4aDir, baseFilename+".m4a")

	filesToUpdate := []string{wavPath, jsonPath, m4aPath}
	for _, path := range filesToUpdate {
		// Check if the file exists before trying to set its time
		if _, err := os.Stat(path); err == nil {
			if err := SetBirthTime(path, t); err != nil {
				log.Printf("Warning: Failed to set file modification time for %s: %v", path, err)
			} else {
				log.Printf("Attempted to set creation time for %s to %s", path, t.Format("2006-01-02 15:04:05"))
			}
		}
	}
}
