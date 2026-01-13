package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

//go:embed templates/*
var templateFS embed.FS

//go:embed icon.svg
var iconFS embed.FS

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

	fmt.Println("Syncing audio times...")
	if err := syncAudioData(); err != nil {
		log.Fatalf("Error syncing audio data: %v", err)
	}
	fmt.Println("Audio time synchronization complete.")

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
	http.HandleFunc("/icon.svg", func(w http.ResponseWriter, r *http.Request) {
		iconContent, err := iconFS.ReadFile("icon.svg")
		if err != nil {
			http.Error(w, "Internal Server Error", 500)
			log.Printf("Error reading embedded icon.svg: %v", err)
			return
		}
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Write(iconContent)
	})
	http.HandleFunc("/", adminHandler)
	http.HandleFunc("/about", aboutHandler)
	http.HandleFunc("/edit-about", editAboutHandler)
	http.HandleFunc("/save-about", saveAboutHandler)
	http.HandleFunc("/edit", editHandler)
	http.HandleFunc("/save", saveHandler)
	http.HandleFunc("/edit-folder", editFolderHandler)
	http.HandleFunc("/save-folder", saveFolderHandler)
	http.HandleFunc("/delete", deleteHandler)
	http.HandleFunc("/generate", generateStaticSiteHandler)
	http.Handle("/site/", http.StripPrefix("/site/", http.FileServer(http.Dir(distDir))))
	fmt.Println("Admin server starting on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
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

	// Sort files within each folder by record date in descending order
	for _, files := range groupedMetadata {
		sort.Slice(files, func(i, j int) bool {
			return files[i].RecordDate.After(files[j].RecordDate)
		})
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

	// Prepare data for the template
	ext := filepath.Ext(metadata.SourceFilename)
	data := EditPageData{
		AudioMetadata: metadata,
		BaseFilename:  strings.TrimSuffix(filepath.Base(metadata.SourceFilename), ext),
		FolderPath:    filepath.Dir(metadata.SourceFilename),
	}

	tmpl, err := template.New("edit.html").Funcs(template.FuncMap{"Base": filepath.Base}).ParseFS(templateFS, "templates/edit.html")
	if err != nil {
		log.Printf("Error parsing template edit.html: %v", err)
		http.Error(w, "Internal Server Error", 500)
		return
	}
	if err := tmpl.Execute(w, data); err != nil {
		log.Printf("Error executing template: %v", err)
		http.Error(w, "Internal Server Error", 500)
	}
}

func saveHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Only POST requests are allowed", http.StatusMethodNotAllowed)
		return
	}
	// --- Get form data ---
	oldSourceFilename := r.FormValue("source_filename")
	if oldSourceFilename == "" {
		http.Error(w, "Source filename is missing", 400)
		return
	}
	folderPath := r.FormValue("folder_path")
	newBaseFilename := r.FormValue("base_filename")

	ext := filepath.Ext(oldSourceFilename) // Get extension once

	// --- Handle Renaming ---
	// Reconstruct the new relative filename
	newSourceFilename := filepath.Join(folderPath, newBaseFilename+ext)
	currentSourceFilename := oldSourceFilename // This will be updated if rename succeeds

	if newSourceFilename != oldSourceFilename {
		log.Printf("Rename requested: %s -> %s", oldSourceFilename, newSourceFilename)

		// Define old and new paths for all related files
		oldWavPath := filepath.Join(wavDir, oldSourceFilename)
		newWavPath := filepath.Join(wavDir, newSourceFilename)
		oldJsonPath := filepath.Join(jsonDir, strings.TrimSuffix(oldSourceFilename, ext)+".json")
		newJsonPath := filepath.Join(jsonDir, strings.TrimSuffix(newSourceFilename, ext)+".json")
		oldM4aPath := filepath.Join(m4aDir, strings.TrimSuffix(oldSourceFilename, ext)+".m4a")
		newM4aPath := filepath.Join(m4aDir, strings.TrimSuffix(newSourceFilename, ext)+".m4a")

		// Helper function to safely rename a file if it exists, and handle target existence
		safeRename := func(oldPath, newPath string, isCritical bool) error {
			_, oldPathExistsErr := os.Stat(oldPath)
			_, newPathExistsErr := os.Stat(newPath)

			if !os.IsNotExist(newPathExistsErr) {
				// Target file already exists and is not an "does not exist" error
				return fmt.Errorf("target file %s already exists", newPath)
			}

			if os.IsNotExist(oldPathExistsErr) {
				// Old file does not exist, nothing to rename, treat as success
				return nil
			}

			// Old file exists, target does not (or is not critical). Attempt rename.
			if err := os.Rename(oldPath, newPath); err != nil {
				return fmt.Errorf("failed to rename %s to %s: %w", oldPath, newPath, err)
			}
			log.Printf("Renamed %s to %s", oldPath, newPath)
			return nil
		}

		// Perform renames with error handling
		if err := safeRename(oldWavPath, newWavPath, true); err != nil {
			log.Printf("Error renaming WAV file: %v", err)
			http.Error(w, fmt.Sprintf("Failed to rename WAV file: %v", err), http.StatusInternalServerError)
			return
		}
		if err := safeRename(oldJsonPath, newJsonPath, true); err != nil {
			log.Printf("Error renaming JSON file: %v", err)
			http.Error(w, fmt.Sprintf("Failed to rename JSON metadata file: %v", err), http.StatusInternalServerError)
			return
		}
		// M4A is cache, if it fails to rename, it's not critical enough to fail the whole save.
		// Just log a warning and continue.
		if err := safeRename(oldM4aPath, newM4aPath, false); err != nil {
			log.Printf("Warning: Failed to rename M4A cache file: %v", err)
		}

		currentSourceFilename = newSourceFilename
	}

	// --- Load and Update Metadata ---
	jsonFileRelPath := strings.TrimSuffix(currentSourceFilename, ext) + ".json"
	jsonFilePath := filepath.Join(jsonDir, jsonFileRelPath)

	metadata, err := loadAudioMetadata(jsonFilePath)
	if err != nil {
		log.Printf("Error getting metadata for %s during save: %v", currentSourceFilename, err)
		http.Error(w, "Audio metadata not found after potential rename.", 404)
		return
	}

	// Update metadata from form
	metadata.SourceFilename = currentSourceFilename // Update to new filename if changed
	aacRelPath := strings.TrimSuffix(currentSourceFilename, ext) + ".m4a"
	metadata.CompressedAudioPath = filepath.ToSlash(filepath.Join("assets", "audio", aacRelPath))
	metadata.Title = strings.ReplaceAll(r.FormValue("title"), "\r", "")
	metadata.Description = strings.ReplaceAll(r.FormValue("description"), "\r", "")
	metadata.Location = strings.ReplaceAll(r.FormValue("location"), "\r", "")

	// --- Handle Time Change ---
	newRecordDateStr := r.FormValue("record_date_date")
	newRecordTimeStr := r.FormValue("record_date_time")

	if newRecordDateStr != "" && newRecordTimeStr != "" {
		dateTimeStr := newRecordDateStr + " " + newRecordTimeStr
		loc := getUserTimeLocation()
		if parsedTime, err := time.ParseInLocation("2006-01-02 15:04:05", dateTimeStr, loc); err != nil {
			log.Printf("Warning: Failed to parse new record date-time '%s' in location %s: %v. Time not changed.", dateTimeStr, loc, err)
		} else {
			log.Printf("Successfully parsed new record_date: %s in location %s", dateTimeStr, loc)
			metadata.RecordDate = parsedTime
			updateAssociatedFileTimestamps(currentSourceFilename, parsedTime)
		}
	}

	// --- Save Final Metadata ---
	updatedJsonContent, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		log.Printf("Failed to marshal json for %s: %v", currentSourceFilename, err)
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

func deleteHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Only POST requests are allowed", http.StatusMethodNotAllowed)
		return
	}
	sourceFilename := r.FormValue("filename")
	if sourceFilename == "" {
		http.Error(w, "Filename parameter is missing", http.StatusBadRequest)
		return
	}

	// Construct file paths
	wavPath := filepath.Join(wavDir, sourceFilename)
	jsonPath := filepath.Join(jsonDir, strings.TrimSuffix(sourceFilename, filepath.Ext(sourceFilename))+".json")
	m4aPath := filepath.Join(m4aDir, strings.TrimSuffix(sourceFilename, filepath.Ext(sourceFilename))+".m4a")

	// Delete the files
	filesToDelete := []string{wavPath, jsonPath, m4aPath}
	for _, path := range filesToDelete {
		err := os.Remove(path)
		if err != nil && !os.IsNotExist(err) {
			log.Printf("Failed to delete file %s: %v", path, err)
			// For robustness, we don't stop, just log the error.
		} else if err == nil {
			log.Printf("Deleted file: %s", path)
		}
	}

	// Check and delete parent directories if they are empty
	dirsToCheck := []string{filepath.Dir(wavPath), filepath.Dir(jsonPath), filepath.Dir(m4aPath)}
	rootDirs := []string{wavDir, jsonDir, m4aDir}
	for i, dir := range dirsToCheck {
		// Ensure we don't delete the root data directories
		if dir != "." && dir != "/" && dir != rootDirs[i] {
			if empty, err := isDirEmpty(dir); err == nil && empty {
				log.Printf("Directory %s is empty, deleting it.", dir)
				if err := os.Remove(dir); err != nil {
					log.Printf("Failed to delete empty directory %s: %v", dir, err)
				}
			} else if err != nil {
				log.Printf("Error checking if directory %s is empty: %v", dir, err)
			}
		}
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

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
			if m4aCacheErr == nil && !m4aCacheInfo.ModTime().Before(srcWavInfo.ModTime()) {
				// M4A cache exists and is not older than WAV, no need to transcode
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
				// Sync file time from WAV to M4A
				wavTime := srcWavInfo.ModTime() // Use ModTime for consistent comparison
				if err := SetBirthTime(m4aCachePath, wavTime); err != nil {
					log.Printf("Warning: Failed to sync file time to M4A cache for %s: %v", m4aCachePath, err)
				}
			}
			currentSourcePath = m4aCachePath

		} else if m4aCacheExists {

			// Scenario 2: WAV does not exist, but M4A cache exists - use cached M4A

			log.Printf("WAV not found for %s. Using M4A from cache.", meta.SourceFilename)

			currentSourcePath = m4aCachePath

			// If the tech info in the JSON is missing, get it from the M4A file and update JSON

			if meta.TechInfo.SampleRate == 0 || meta.DurationSeconds == 0 {

				log.Printf("Re-evaluating tech info from M4A cache for %s...", meta.SourceFilename)

				duration, sampleRate, bitDepth, channels, err := getAudioTechInfo(currentSourcePath)

				if err != nil {

					log.Printf("Warning: Failed to get tech info from M4A cache %s: %v", currentSourcePath, err)

				} else {

					meta.DurationSeconds = duration

					meta.TechInfo.SampleRate = sampleRate

					meta.TechInfo.BitDepth = bitDepth

					meta.TechInfo.Channels = channels

					log.Printf("Updating JSON file for %s with info from M4A cache.", meta.SourceFilename)

					originalJsonPath := filepath.Join(jsonDir, strings.TrimSuffix(meta.SourceFilename, filepath.Ext(meta.SourceFilename))+".json")

					updatedJsonContent, err := json.MarshalIndent(meta, "", "  ")

					if err != nil {

						log.Printf("Warning: Failed to marshal updated metadata for %s: %v", meta.SourceFilename, err)

					} else {

						if err := os.WriteFile(originalJsonPath, updatedJsonContent, 0644); err != nil {

							log.Printf("Warning: Failed to write updated JSON file %s: %v", originalJsonPath, err)

						}

					}

				}

			}

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
