package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kleinai/backend/internal/provider/grok"
	"github.com/kleinai/backend/pkg/crypto"
)

func main() {
	credHex := strings.TrimSpace(os.Getenv("PROBE_CRED_HEX"))
	aesKey := strings.TrimSpace(os.Getenv("KLEIN_AES_KEY"))
	refFile := strings.TrimSpace(os.Getenv("PROBE_REF_FILE"))
	proxyURL := strings.TrimSpace(os.Getenv("PROBE_PROXY_URL"))
	if credHex == "" || aesKey == "" || refFile == "" {
		fail("missing PROBE_CRED_HEX or KLEIN_AES_KEY or PROBE_REF_FILE")
	}

	credCipher, err := hex.DecodeString(credHex)
	if err != nil {
		fail(fmt.Sprintf("decode cred hex: %v", err))
	}
	key := []byte(aesKey)
	if len(key) != 32 {
		if decoded, err := hex.DecodeString(aesKey); err == nil && len(decoded) == 32 {
			key = decoded
		} else {
			fail(fmt.Sprintf("aes key must be 32 bytes raw or 64 hex chars, got %d", len(key)))
		}
	}
	aesgcm, err := crypto.NewAESGCM(key)
	if err != nil {
		fail(fmt.Sprintf("new aes: %v", err))
	}
	plain, err := aesgcm.Decrypt(credCipher)
	if err != nil {
		fail(fmt.Sprintf("decrypt credential: %v", err))
	}
	cred := strings.TrimSpace(string(plain))
	if cred == "" {
		fail("empty credential")
	}

	dataURL, err := fileToDataURL(refFile)
	if err != nil {
		fail(fmt.Sprintf("fileToDataURL: %v", err))
	}

	web := grok.NewWebClientWithProxy("", proxyURL)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	fileID, assetURL, err := web.UploadProbeImage(ctx, cred, dataURL)
	if err != nil {
		fail(fmt.Sprintf("upload probe image: %v", err))
	}
	fmt.Printf("file_id=%s\nasset_url=%s\n", fileID, assetURL)
}

func fileToDataURL(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	ext := strings.ToLower(filepath.Ext(path))
	mimeType := mime.TypeByExtension(ext)
	if mimeType == "" {
		switch ext {
		case ".jpg", ".jpeg":
			mimeType = "image/jpeg"
		case ".png":
			mimeType = "image/png"
		case ".webp":
			mimeType = "image/webp"
		default:
			mimeType = "application/octet-stream"
		}
	}
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}
