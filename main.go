package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/go-redis/redis/v8"
	rotatelogs "github.com/lestrrat-go/file-rotatelogs"
)

var (
	redisClient *redis.Client
)

type Encounter struct {
	ResourceType string `json:"resourceType"`
	ID           string `json:"id"`
	Status       string `json:"status"`
	Class        struct {
		System string `json:"system"`
		Code   string `json:"code"`
	} `json:"class"`
	Period struct {
		Start time.Time `json:"start"`
		End   time.Time `json:"end"`
	} `json:"period"`
	Participant []struct {
		Individual struct {
			Reference string `json:"reference"`
		} `json:"individual"`
	} `json:"participant"`
	Subject struct {
		Reference string `json:"reference"`
	} `json:"subject"`
}

type EncounterDB struct {
	FhirId         string `json:"fhirId"`
	FullUrl        string `json:"fullUrl"`
	Status         string `json:"status"`
	Class          string `json:"class"`
	Period         Period `json:"period"`
	PractitionerId string `json:"practitionerId"`
	PatientId      string `json:"patientId"`
}

type Period struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end,omitempty"`
}

type Bundle struct {
	Entry []struct {
		FullUrl  string    `json:"fullUrl"`
		Resource Encounter `json:"resource"`
	} `json:"entry"`
}

type Practitioner struct {
	ResourceType string `json:"resourceType"`
	ID           string `json:"id"`
	Name         []struct {
		Family string   `json:"family"`
		Given  []string `json:"given"`
	} `json:"name"`
}

type PractitionerDB struct {
	FhirId     string `json:"fhirId"`
	GivenName  string `json:"givenName"`
	FamilyName string `json:"familyName"`
}

type Patient struct {
	ResourceType string `json:"resourceType"`
	ID           string `json:"id"`
	Name         []struct {
		Family string   `json:"family"`
		Given  []string `json:"given"`
	} `json:"name"`
	BirthDate string `json:"birthDate"`
	Gender    string `json:"gender"`
}

type PatientDB struct {
	FhirId     string `json:"id"`
	GivenName  string `json:"givenName"`
	FamilyName string `json:"familyName"`
	BirthDate  string `json:"birthDate"`
	Gender     string `json:"gender"`
}

type FHIRMessage struct {
	Encounter    EncounterDB    `json:"encounter"`
	Practitioner PractitionerDB `json:"practitioner"`
	Patient      PatientDB      `json:"patient"`
}

func fetchData(ctx context.Context, url string) ([]byte, error) {
	log.Printf("Making request to URL: %s", url)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("error creating request: %w", err)
	}

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error calling API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading API response: %w", err)
	}

	return body, nil
}

func extractReferenceID(ref string) string {
	parts := strings.Split(ref, "/")
	if len(parts) > 1 {
		return parts[len(parts)-1]
	}
	return ref
}

func processEncounter(ctx context.Context, enc Encounter, fullUrl string, clientID string) {
	if enc.Status == "" || enc.Class.Code == "" || enc.Participant == nil || enc.Subject.Reference == "" || fullUrl == "" {
		log.Printf("Invalid encounter found, adding to invalid_encounters set: %s", fullUrl)
		_, err := redisClient.SAdd(ctx, "invalid_encounters", fullUrl).Result()
		if err != nil {
			log.Printf("Error adding to invalid_encounters: %v", err)
		}
		return
	}

	practitionerRef := enc.Participant[0].Individual.Reference
	if practitionerRef == "" {
		log.Printf("Nenhuma referência de practitioner encontrada para encontro: %s", enc.ID)
		return
	}
	practitionerId := extractReferenceID(enc.Participant[0].Individual.Reference)

	patientRef := enc.Subject.Reference
	if patientRef == "" {
		log.Printf("Nenhuma referência de paciente encontrada para encontro: %s", enc.ID)
		return
	}
	patientId := extractReferenceID(enc.Subject.Reference)

	encParsed := EncounterDB{
		FhirId:  enc.ID,
		FullUrl: fullUrl,
		Status:  enc.Status,
		Class:   enc.Class.Code,
		Period: Period{
			Start: enc.Period.Start,
			End:   enc.Period.End,
		},
		PractitionerId: practitionerId,
		PatientId:      patientId,
	}

	practitionerURL := fmt.Sprintf("https://hapi.fhir.org/baseR4/%s", practitionerRef)
	log.Printf("Buscando practitioner de: %s", practitionerURL)
	practitionerData, err := fetchDataWithRetry(ctx, practitionerURL, 3)
	if err != nil {
		log.Printf("Erro ao buscar practitioner após 3 tentativas: %v", err)
		redisClient.SAdd(ctx, "invalid_encounters", fullUrl).Result()
		return
	}

	var practitioner Practitioner
	if err := json.Unmarshal(practitionerData, &practitioner); err != nil {
		log.Printf("Erro ao parsear JSON do practitioner: %v", err)
		redisClient.SAdd(ctx, "invalid_encounters", fullUrl).Result()
		return
	}

	if !(len(practitioner.Name) > 0 && len(practitioner.Name[0].Given) > 0) {
		log.Printf("Practitioner inválido: %s", practitionerRef)
		redisClient.SAdd(ctx, "invalid_encounters", fullUrl).Result()
		return
	}

	practitionerParsed := PractitionerDB{
		FhirId:     practitioner.ID,
		GivenName:  practitioner.Name[0].Given[0],
		FamilyName: practitioner.Name[0].Family,
	}

	patientURL := fmt.Sprintf("https://hapi.fhir.org/baseR4/%s", patientRef)
	log.Printf("Buscando paciente de: %s", patientURL)
	patientData, err := fetchDataWithRetry(ctx, patientURL, 3)
	if err != nil {
		log.Printf("Erro ao buscar paciente após 3 tentativas: %v", err)
		redisClient.SAdd(ctx, "invalid_encounters", fullUrl).Result()
		return
	}

	var patient Patient
	if err := json.Unmarshal(patientData, &patient); err != nil {
		log.Printf("Erro ao parsear JSON do paciente: %v", err)
		redisClient.SAdd(ctx, "invalid_encounters", fullUrl).Result()
		return
	}

	if !(len(patient.Name) > 0 && len(patient.Name[0].Given) > 0) {
		log.Printf("Patient inválido: %s", practitionerRef)
		redisClient.SAdd(ctx, "invalid_encounters", fullUrl).Result()
		return
	}

	patientParsed := PatientDB{
		FhirId:     patient.ID,
		GivenName:  patient.Name[0].Given[0],
		FamilyName: patient.Name[0].Family,
		BirthDate:  patient.BirthDate,
		Gender:     patient.Gender,
	}

	message := FHIRMessage{
		Encounter:    encParsed,
		Practitioner: practitionerParsed,
		Patient:      patientParsed,
	}

	jsonMsg, err := json.MarshalIndent(message, "", "  ")
	log.Printf("Mensagem sendo enviada: %v", string(jsonMsg))

	if err := sendToSQS(ctx, message, clientID); err != nil {
		log.Printf("Erro ao enviar mensagem para SQS: %v", err)
		redisClient.SAdd(ctx, "invalid_encounters", fullUrl).Result()
	}
}

func sendToSQS(ctx context.Context, message FHIRMessage, clientID string) error {
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion("sa-east-1"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
		config.WithEndpointResolverWithOptions(aws.EndpointResolverWithOptionsFunc(
			func(service, region string, options ...interface{}) (aws.Endpoint, error) {
				return aws.Endpoint{
					URL:           "http://localstack:4566",
					SigningRegion: "sa-east-1",
				}, nil
			},
		)),
	)

	if err != nil {
		return fmt.Errorf("error loading AWS config: %w", err)
	}

	sqsClient := sqs.NewFromConfig(cfg)

	queueURL := os.Getenv("SQS_QUEUE_URL")
	if queueURL == "" {
		log.Fatal("SQS_QUEUE_URL is empty.")
	}

	msgBody, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("error converting message to JSON: %w", err)
	}

	log.Printf("Sending message to SQS for client %s", clientID)
	_, err = sqsClient.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:       aws.String(queueURL),
		MessageBody:    aws.String(string(msgBody)),
		MessageGroupId: aws.String(clientID),
	})
	if err != nil {
		return fmt.Errorf("error sending message to SQS: %w", err)
	}

	log.Printf("Message successfully sent to SQS for client %s", clientID)
	return nil
}

func processDate(ctx context.Context, date string) error {
	log.Printf("Processing date: %s", date)
	url := fmt.Sprintf("https://hapi.fhir.org/baseR4/Encounter?date=%s", date)

	const maxRetries = 3
	data, err := fetchDataWithRetry(ctx, url, maxRetries)
	if err != nil {
		log.Printf("Add failed date to unprocessed_dates: %v", date, err)
		_, redisErr := redisClient.SAdd(ctx, "unprocessed_dates", date).Result()
		if redisErr != nil {
			log.Printf("Erro ao adicionar data não processada no Redis: %v", redisErr)
		}
		return fmt.Errorf("falha ao processar data %s: %w", date, err)
	}

	var bundle Bundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		return fmt.Errorf("erro ao parsear JSON de encontros: %w", err)
	}

	if len(bundle.Entry) == 0 {
		log.Printf("Nenhum encontro encontrado para a data: %s", date)
		return nil
	}

	var wg sync.WaitGroup
	for i, entry := range bundle.Entry {
		wg.Add(1)
		clientID := "001"
		if i%2 == 1 {
			clientID = "002"
		}

		go func(enc Encounter, fullUrl string, clientID string) {
			defer wg.Done()
			processEncounter(ctx, enc, fullUrl, clientID)
		}(entry.Resource, entry.FullUrl, clientID)
	}

	wg.Wait()
	return nil
}

func fetchDataWithRetry(ctx context.Context, url string, maxRetries int) ([]byte, error) {
	for i := 0; i < maxRetries; i++ {
		if i > 0 {
			waitTime := time.Second * time.Duration(1<<uint(i))
			log.Printf("Re-trying %d/%d in %v...", i, maxRetries, waitTime)
			time.Sleep(waitTime)
		}

		data, err := fetchData(ctx, url)
		if err == nil {
			return data, nil
		}

		log.Printf("Attempt %d/%d failed to request %s: %v", i+1, maxRetries, url, err)
	}
	return nil, fmt.Errorf("All attempts were failed")
}

func initLogger() {
	writer, err := rotatelogs.New(
		"/app/logs/logs/collector.%Y-%m-%d.log",
		rotatelogs.WithLinkName("/app/logs/collector.log"),
		rotatelogs.WithRotationTime(24*time.Hour),
		rotatelogs.WithMaxAge(72*time.Hour),
	)
	if err != nil {
		log.Fatalf("Erro ao configurar rotação de logs: %v", err)
	}
	multi := io.MultiWriter(os.Stdout, writer)
	log.SetOutput(multi)
}

func initCache() {
	valkeyUri := os.Getenv("VALKEY_URI")
	// valkeyPwd := os.Getenv("VALKEY_PWD")

	redisClient = redis.NewClient(&redis.Options{
		Addr: valkeyUri,
		// Password: valkeyPwd,
		DB: 0,
	})

}

func main() {
	ctx := context.Background()
	initLogger()
	initCache()
	startDateStr := os.Getenv("START_DATE")
	endDateStr := os.Getenv("END_DATE")

	if startDateStr == "" || endDateStr == "" {
		log.Fatal("START_DATE and END_DATE environment variables are required")
	}

	startDate, err := time.Parse("2006-01-02", startDateStr)
	if err != nil {
		log.Fatalf("Invalid START_DATE format: %v", err)
	}

	endDate, err := time.Parse("2006-01-02", endDateStr)
	if err != nil {
		log.Fatalf("Invalid END_DATE format: %v", err)
	}

	log.Printf("Checking previous date processed in cache")
	lastProcessedDateStr, err := redisClient.Get(ctx, "last_processed_date").Result()
	if err != nil && err != redis.Nil {
		log.Fatalf("Error getting last processed date from Redis: %v", err)
	}

	var currentDate time.Time
	if lastProcessedDateStr == "" {
		currentDate = startDate
		log.Printf("No last processed date found, starting from START_DATE: %s", startDateStr)
	} else {
		currentDate, err = time.Parse("2006-01-02", lastProcessedDateStr)
		if err != nil {
			log.Fatalf("Invalid last processed date format in cache: %v", err)
		}
		log.Printf("Resuming from last processed date: %s", lastProcessedDateStr)
	}
	log.Printf("**** currentDate **** %v", currentDate)

	for {
		if currentDate.After(endDate) {
			log.Printf("Reached END_DATE (%s), stopping processing", endDateStr)
			log.Println("Processing completed")
			break

		} else {
			dateStr := currentDate.Format("2006-01-02")
			err := processDate(ctx, dateStr)
			if err != nil {
				log.Printf("Error processing date %s: %v", dateStr, err)
			} else {
				_, err := redisClient.Set(ctx, "last_processed_date", dateStr, 0).Result()
				if err != nil {
					log.Printf("Error updating last processed date in Redis: %v", err)
				}
				currentDate = currentDate.Add(24 * time.Hour)
			}
		}
	}
	defer redisClient.Close()
	log.Println("Finish!")
}
