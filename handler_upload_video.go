package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	// Set limit of 1GB on file upload
	r.Body = http.MaxBytesReader(w, r.Body, 1<<30)

	// Get video id from path
	videoIdString := r.PathValue("videoID")

	// Convert video id from string to uuid
	videoId, err := uuid.Parse(videoIdString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	// Get jwt token
	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	// Validate jwt and get user id from it
	userId, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	// Get metadata of video from db using video id
	videoMetadata, err := cfg.db.GetVideo(videoId)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't find video metadata", err)
		return
	}

	// Check if user is owner of the video
	if videoMetadata.UserID != userId {
		respondWithError(w, http.StatusUnauthorized, "User not authorized", err)
		return
	}

	// Get the uploaded video info
	videoFile, header, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse video file", err)
		return
	}
	defer videoFile.Close()

	// Get media type of uploaded video
	mediaType, _, err := mime.ParseMediaType(header.Header.Get("Content-Type"))
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	// Get file extension
	extension := strings.Split(mediaType, "/")[1]

	// Check if mp4 is uploaded
	if mediaType != "video/mp4" {
		respondWithError(w, http.StatusBadRequest, "Invalid file upload", err)
		return
	}

	// Create temp file
	tempFile, err := os.CreateTemp("", "tubely-upload.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't create temp file", err)
		return
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	// Copy video data into tempfile
	_, err = io.Copy(tempFile, videoFile)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't write video data", err)
		return
	}

	// Move pointer to beginning to read again
	tempFile.Seek(0, io.SeekStart)

	//Generate random video name
	videoRandomName := make([]byte, 32)
	_, err = rand.Read(videoRandomName)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't generate random name", err)
		return
	}

	aspectRatio, err := getVideoAspectRatio(tempFile.Name())
	directory := ""
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't get aspect ratio", err)
		return
	}

	switch aspectRatio {
	case "16:9":
		directory = "landscape"
	case "9:16":
		directory = "portrait"
	default:
		directory = "other"
	}

	// Encode video name
	prefix := fmt.Sprintf("%s/", directory)
	encodedVideoName := prefix + base64.RawURLEncoding.EncodeToString(videoRandomName) + "." + extension

	// Upload to S3
	_, err = cfg.s3Client.PutObject(r.Context(), &s3.PutObjectInput{
		Bucket:      &cfg.s3Bucket,
		Key:         &encodedVideoName,
		Body:        tempFile,
		ContentType: &mediaType,
	})
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't upload to S3", err)
		return
	}

	// Updating Video URL
	videoURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", cfg.s3Bucket, cfg.s3Region, encodedVideoName)
	videoMetadata.VideoURL = &videoURL

	err = cfg.db.UpdateVideo(videoMetadata)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't update video", err)
		return
	}

	respondWithJSON(w, http.StatusOK, videoMetadata)
}

func getVideoAspectRatio(filePath string) (string, error) {
	var out bytes.Buffer
	type FFProbeOutput struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
		} `json:"streams"`
	}

	cmd := exec.Command("ffprobe", "-v", "error", "-print_format", "json", "-show_streams", filePath)
	cmd.Stdout = &out
	cmd.Run()

	data := FFProbeOutput{}
	err := json.Unmarshal(out.Bytes(), &data)
	if err != nil {
		return "", err
	}

	var width, height int
	for _, stream := range data.Streams {
		if stream.CodecType == "video" {
			width = stream.Width
			height = stream.Height
		}
	}

	if width == 16*height/9 {
		return "16:9", nil
	} else if height == 16*width/9 {
		return "9:16", nil
	}
	return "other", nil
}
