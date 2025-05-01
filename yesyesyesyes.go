package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/joho/godotenv"
)

var (
	itemURL        string
	cacheFile      string
	botToken       string
	chatID         string
	languageCookie string
)

type Cache struct {
	Availability string `json:"availability"`
	Alerts       string `json:"alerts"`
}

func init() {
	err := godotenv.Load()
	if err != nil {
		log.Println("No .env file found, relying on system environment variables.")
	}

	itemURL = os.Getenv("ITEM_URL")
	cacheFile = os.Getenv("CACHE_FILE")
	botToken = os.Getenv("BOT_TOKEN")
	chatID = os.Getenv("CHAT_ID")
	languageCookie = os.Getenv("LANGUAGE_COOKIE")

	if itemURL == "" || cacheFile == "" || botToken == "" || chatID == "" || languageCookie == "" {
		log.Fatal("Missing required environment variables. Please check your .env file or system environment.")
	}
}

func fetchAvailabilityAndAlerts(url string) (string, string, error) {
	client := &http.Client{}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", "", err
	}

	req.AddCookie(&http.Cookie{
		Name:  "pretix_language",
		Value: languageCookie,
	})
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Ubuntu; Linux x86_64; rv:138.0) Gecko/20100101 Firefox/138.0")

	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", "", fmt.Errorf("received non-200 response code: %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return "", "", err
	}

	item := doc.Find("article#item-350")
	if item.Length() == 0 {
		return "", "", fmt.Errorf("item 350 not found on the page")
	}

	availability := item.Find(".availability-box")
	status := "IN STOCK"
	if availability.HasClass("gone") {
		status = "SOLD OUT"
	}

	var alerts []string
	doc.Find("div.alert").Each(func(i int, s *goquery.Selection) {
		text := strings.TrimSpace(s.Text())
		alerts = append(alerts, text)
	})

	return status, strings.Join(alerts, "\n---\n"), nil
}

func readCache(path string) Cache {
	f, err := os.Open(path)
	if err != nil {
		return Cache{}
	}
	defer f.Close()

	var data Cache
	if err := json.NewDecoder(f).Decode(&data); err != nil && err != io.EOF {
		log.Printf("Failed to parse cache: %v", err)
		return Cache{}
	}

	return data
}

func writeCache(path string, data Cache) {
	f, err := os.Create(path)
	if err != nil {
		log.Printf("Failed to write cache: %v", err)
		return
	}
	defer f.Close()

	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(data)
}

func sendTelegramMessage(message string) {
	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", botToken)
	values := url.Values{}
	values.Set("chat_id", chatID)
	values.Set("text", message)

	const maxRetries = 3
	backoff := time.Second

	for i := 1; i <= maxRetries; i++ {
		resp, err := http.PostForm(endpoint, values)
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			return
		}
		if resp != nil {
			resp.Body.Close()
		}

		log.Printf("Telegram send failed (attempt %d): %v", i, err)
		if i < maxRetries {
			time.Sleep(backoff)
			backoff *= 2
		}
	}
}

func main() {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown-host"
	}

	currentStatus, currentAlerts, err := fetchAvailabilityAndAlerts(itemURL)
	if err != nil {
		errorMsg := fmt.Sprintf("❌ [%s] Error fetching data: %v", hostname, err)
		log.Println(errorMsg)
		sendTelegramMessage(errorMsg)
		return
	}

	cache := readCache(cacheFile)
	updated := false

	if currentStatus != cache.Availability {
		sendTelegramMessage(fmt.Sprintf("🔔 [%s] Availability changed: %s\n%s", hostname, currentStatus, itemURL))
		cache.Availability = currentStatus
		updated = true
	}

	if currentAlerts != cache.Alerts {
		sendTelegramMessage(fmt.Sprintf("📢 [%s] Alert messages changed:\n%s", hostname, currentAlerts))
		cache.Alerts = currentAlerts
		updated = true
	}

	if updated {
		writeCache(cacheFile, cache)
		log.Println("Status and/or alerts updated.")
	} else {
		log.Println("No changes detected.")
	}

}
