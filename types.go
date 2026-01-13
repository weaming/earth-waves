package main

import "time"

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

// EditPageData is used to pass data to the edit.html template
type EditPageData struct {
	AudioMetadata
	BaseFilename string
	FolderPath   string
}

// AudioMetadata 定义了音频文件的元数据结构
type AudioMetadata struct {
	SourceFilename       string    `json:"source_filename"`
	Title                string    `json:"title"`
	Description          string    `json:"description"`
	Location             string    `json:"location"`
	RecordDate           time.Time `json:"record_date"` // Use default time.Time
	DurationSeconds      float64   `json:"duration_seconds"`
	SourceFileSizeMB     float64   `json:"source_file_size_mb"`     // 源文件大小(MB)
	CompressedFileSizeMB float64   `json:"compressed_file_size_mb"` // 压缩后文件大小(MB)
	CompressedAudioPath  string    `json:"compressed_audio_path"`   // 相对于dist目录的路径
	TechInfo             struct {
		SampleRate int `json:"sample_rate"`
		BitDepth   int `json:"bit_depth"`
		Channels   int `json:"channels"`
	} `json:"tech_info"`
}
