package deepstylelib

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
)

// Doc types
const (
	Job = "job"
)

// Job States
const (
	StateNotReadyToProcess    = "NOT_READY_TO_PROCESS"  // no attachments yet
	StateReadyToProcess       = "READY_TO_PROCESS"      // attachments added
	StateBeingProcessed       = "BEING_PROCESSED"       // worker running
	StateProcessingSuccessful = "PROCESSING_SUCCESSFUL" // worker done
	StateProcessingFailed     = "PROCESSING_FAILED"     // processing failed
)

type Attachments map[string]interface{}

type Document struct {
	Revision string `json:"_rev"`
	Id       string `json:"_id"`
}

type TypedDocument struct {
	Document
	Type string `json:"type"`
}

func (doc TypedDocument) IsJob() bool {
	return doc.Type == Job
}

type JobDocument struct {
	TypedDocument
	Attachments      Attachments `json:"_attachments"`
	State            string      `json:"state"`
	CreatedAt        string      `json:"created_at"`
	Owner            string      `json:"owner"`
	OwnerDeviceToken string      `json:"owner_devicetoken"`
	ErrorMessage     string      `json:"error_message"`
	StdOutAndErr     string      `json:"std_out_and_err"`
	config           configuration
}

func NewJobDocument(documentId string, config configuration) (jobDocument *JobDocument, err error) {
	jobDocument = &JobDocument{
		config: config,
	}
	jobDocument.Id = documentId
	err = jobDocument.RefreshFromDB()
	return jobDocument, err
}

func (doc JobDocument) IsReadyToProcess() bool {
	return doc.State == StateReadyToProcess
}

func (doc JobDocument) IsProcessingSuccessful() bool {
	return doc.State == StateProcessingSuccessful
}

func (doc JobDocument) IsProcessingFailed() bool {
	return doc.State == StateProcessingFailed
}

func (doc *JobDocument) SetStdOutAndErr(stdOutAndErr string) (updated bool, err error) {

	db := doc.config.Database
	if stdOutAndErr == "" {
		return false, nil
	}

	retryUpdater := func() {
		doc.StdOutAndErr = stdOutAndErr
	}

	retryDoneMetric := func() bool {
		return doc.StdOutAndErr == stdOutAndErr
	}

	retryRefresh := func() error {
		return doc.RefreshFromDB()
	}

	return db.EditRetry(
		doc,
		retryUpdater,
		retryDoneMetric,
		retryRefresh,
	)

}

func (doc *JobDocument) UpdateState(newState string) (updated bool, err error) {

	db := doc.config.Database

	retryUpdater := func() {
		doc.State = newState
	}

	retryDoneMetric := func() bool {
		return doc.State == newState
	}

	retryRefresh := func() error {
		return doc.RefreshFromDB()
	}

	return db.EditRetry(
		doc,
		retryUpdater,
		retryDoneMetric,
		retryRefresh,
	)

}

func (doc *JobDocument) SetErrorMessage(errorMessage error) (updated bool, err error) {

	db := doc.config.Database

	if errorMessage.Error() == "" {
		return false, nil
	}

	retryUpdater := func() {
		doc.ErrorMessage = errorMessage.Error()
	}

	retryDoneMetric := func() bool {
		return doc.ErrorMessage == errorMessage.Error()
	}

	retryRefresh := func() error {
		return doc.RefreshFromDB()
	}

	return db.EditRetry(
		doc,
		retryUpdater,
		retryDoneMetric,
		retryRefresh,
	)

}

func (doc *JobDocument) RetrieveAttachment(attachmentName string) (io.Reader, error) {
	db := doc.config.Database
	return db.RetrieveAttachment(doc.Id, attachmentName)
}

func (doc *JobDocument) SetConfiguration(config configuration) {
	doc.config = config
}

func (doc *JobDocument) RefreshFromDB() error {

	db := doc.config.Database
	jobDoc := JobDocument{}

	// if we don't do this, the new doc won't have the config
	// with the db url.
	jobDoc.SetConfiguration(doc.config)

	err := db.Retrieve(doc.Id, &jobDoc)
	if err != nil {
		return err
	}
	*doc = jobDoc
	return nil
}

func (doc *JobDocument) AddAttachment(attachmentName, filepath string) (err error) {

	db := doc.config.Database
	dbUrl := db.DBURL()

	endpointUrlStr := fmt.Sprintf("%v/%v/%v",
		dbUrl,
		doc.Id,
		attachmentName,
	)

	client := &http.Client{}

	f, err := os.Open(filepath)
	if err != nil {
		return err
	}
	defer f.Close()

	reader := bufio.NewReader(f)

	for i := 1; i <= 10; i++ {

		// get latest revision of doc so we are updating the current rev
		if err := doc.RefreshFromDB(); err != nil {
			return err
		}

		endpointUrlStr = fmt.Sprintf("%v?rev=%v", endpointUrlStr, doc.Revision)
		log.Printf("endpointUrlStr: %v", endpointUrlStr)

		req, err := http.NewRequest("PUT", endpointUrlStr, reader)
		if err != nil {
			return err
		}

		req.Header.Set("Content-Type", "image/png")

		resp, err := client.Do(req)
		if err != nil {
			return err
		}

		resp.Body.Close()

		if resp.StatusCode == 409 {
			log.Printf("409 conflict, retrying attempt #%v", i+1)
			continue
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("Unable to upload attachment: %v from %v. Unexpected status code in response: %v", attachmentName, filepath, resp.StatusCode)
		}

		return nil

	}

	return fmt.Errorf("Tried to add attachment 10 times, giving up")

}
