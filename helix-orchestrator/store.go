package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const (
	skActive   = "A"
	pkQueue    = "queue"
	opTimeout  = 5 * time.Second
	claimTries = 8
)

var (
	errStatus  = errors.New("status must be success or failure")
	errMissing = errors.New("not found")
)

type storedKey struct {
	ID        string `json:"id"`
	Role      string `json:"role"`
	Name      string `json:"name"`
	Prefix    string `json:"prefix"`
	Hash      string `json:"hash"`
	CreatedAt string `json:"createdAt"`
}

type keyResponse struct {
	ID        string `json:"id"`
	Role      string `json:"role"`
	Name      string `json:"name"`
	Prefix    string `json:"prefix"`
	CreatedAt string `json:"createdAt"`
	Token     string `json:"token,omitempty"`
}

type request struct {
	UID       string         `json:"uid"`
	OS        string         `json:"os"`
	URL       string         `json:"url,omitempty"`
	Token     string         `json:"token,omitempty"`
	UserKeyID string         `json:"userKeyId"`
	Status    string         `json:"status"`
	Result    map[string]any `json:"result"`
	CreatedAt string         `json:"createdAt"`
	UpdatedAt string         `json:"updatedAt"`
}

type accessRow struct {
	UID       string `json:"uid"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	CreatedAt string `json:"createdAt"`
	URL       string `json:"url"`
	Token     string `json:"token"`
	RDPUser   string `json:"rdpUsername"`
	RDPPass   string `json:"rdpPassword"`
	Key       string `json:"key"`
	UpdatedAt string `json:"updatedAt"`
}

type queueRow struct {
	UID       string         `json:"uid"`
	OS        string         `json:"os"`
	Status    string         `json:"status"`
	CreatedAt string         `json:"createdAt"`
	UpdatedAt string         `json:"updatedAt"`
	Result    map[string]any `json:"result"`
	Viewer    string         `json:"viewer,omitempty"`
}

type microvmRecord struct {
	ID           string `json:"microvmId"`
	Endpoint     string `json:"endpoint"`
	URL          string `json:"url,omitempty"`
	Region       string `json:"region,omitempty"`
	State        string `json:"state"`
	StateReason  string `json:"stateReason,omitempty"`
	ImageArn     string `json:"imageArn,omitempty"`
	ImageVersion string `json:"imageVersion,omitempty"`
	StartedAt    string `json:"startedAt,omitempty"`
	CreatedAt    string `json:"createdAt"`
	UpdatedAt    string `json:"updatedAt"`
	Token        string `json:"token,omitempty"`
	Key          string `json:"key,omitempty"`
	UID          string `json:"uid,omitempty"`
	Viewer       string `json:"viewer,omitempty"`
	DesktopError string `json:"desktopError,omitempty"`
}

type fileState struct {
	Keys     []storedKey `json:"keys"`
	Requests []request   `json:"requests"`
	Access   []accessRow `json:"access"`
}

type importCounts struct {
	Keys     int
	Requests int
	Queued   int
	Access   int
}

// jsonMap stores a worker result as JSON so numbers and nesting round-trip
// the way encoding/json produced them in the old state file.
type jsonMap map[string]any

func (m jsonMap) MarshalDynamoDBAttributeValue() (types.AttributeValue, error) {
	if m == nil {
		m = jsonMap{}
	}
	body, err := json.Marshal(map[string]any(m))
	if err != nil {
		return nil, err
	}
	return &types.AttributeValueMemberS{Value: string(body)}, nil
}

func (m *jsonMap) UnmarshalDynamoDBAttributeValue(av types.AttributeValue) error {
	if av == nil {
		*m = nil
		return nil
	}
	switch value := av.(type) {
	case *types.AttributeValueMemberNULL:
		*m = nil
		return nil
	case *types.AttributeValueMemberS:
		if value.Value == "" {
			*m = jsonMap{}
			return nil
		}
		decoded := map[string]any{}
		if err := json.Unmarshal([]byte(value.Value), &decoded); err != nil {
			return err
		}
		*m = decoded
		return nil
	default:
		return fmt.Errorf("result is %T", av)
	}
}

type dbItem struct {
	PK           string  `dynamodbav:"pk"`
	SK           string  `dynamodbav:"sk"`
	ID           string  `dynamodbav:"id,omitempty"`
	Role         string  `dynamodbav:"role,omitempty"`
	Name         string  `dynamodbav:"name,omitempty"`
	Prefix       string  `dynamodbav:"prefix,omitempty"`
	Hash         string  `dynamodbav:"hash,omitempty"`
	CreatedAt    string  `dynamodbav:"createdAt,omitempty"`
	UID          string  `dynamodbav:"uid,omitempty"`
	OS           string  `dynamodbav:"os,omitempty"`
	URL          string  `dynamodbav:"url,omitempty"`
	Token        string  `dynamodbav:"token,omitempty"`
	UserKeyID    string  `dynamodbav:"userKeyId,omitempty"`
	Status       string  `dynamodbav:"status,omitempty"`
	Result       jsonMap `dynamodbav:"result,omitempty"`
	UpdatedAt    string  `dynamodbav:"updatedAt,omitempty"`
	Username     string  `dynamodbav:"username,omitempty"`
	Password     string  `dynamodbav:"password,omitempty"`
	RDPUser      string  `dynamodbav:"rdpUsername,omitempty"`
	RDPPass      string  `dynamodbav:"rdpPassword,omitempty"`
	Key          string  `dynamodbav:"key,omitempty"`
	MicrovmID    string  `dynamodbav:"microvmId,omitempty"`
	Endpoint     string  `dynamodbav:"endpoint,omitempty"`
	Region       string  `dynamodbav:"region,omitempty"`
	State        string  `dynamodbav:"state,omitempty"`
	StateReason  string  `dynamodbav:"stateReason,omitempty"`
	ImageArn     string  `dynamodbav:"imageArn,omitempty"`
	ImageVersion string  `dynamodbav:"imageVersion,omitempty"`
	StartedAt    string  `dynamodbav:"startedAt,omitempty"`
}

type storeConfig struct {
	Table    string
	Region   string
	Endpoint string
}

type Store struct {
	db       *dynamodb.Client
	table    string
	endpoint string
}

func openStoreFromEnv() (*Store, error) {
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = os.Getenv("AWS_DEFAULT_REGION")
	}
	return openStore(storeConfig{
		Table:    os.Getenv("DYNAMODB_TABLE"),
		Region:   region,
		Endpoint: os.Getenv("DYNAMODB_ENDPOINT"),
	})
}

func openStore(cfg storeConfig) (*Store, error) {
	if cfg.Table == "" {
		return nil, errors.New("DYNAMODB_TABLE is required")
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	loadCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	opts := []func(*config.LoadOptions) error{config.WithRegion(cfg.Region)}
	if cfg.Endpoint != "" {
		opts = append(opts, config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("local", "local", "")))
	}
	awsCfg, err := config.LoadDefaultConfig(loadCtx, opts...)
	if err != nil {
		return nil, err
	}
	if cfg.Endpoint != "" {
		// DynamoDB Local rejects the default request checksum headers.
		awsCfg.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		awsCfg.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
	}
	client := dynamodb.NewFromConfig(awsCfg, func(o *dynamodb.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			// DynamoDB Local sends an X-Amz-Crc32 header the SDK cannot verify.
			o.DisableValidateResponseChecksum = true
		}
	})
	store := &Store{db: client, table: cfg.Table, endpoint: cfg.Endpoint}
	if cfg.Endpoint != "" {
		if err := store.ensureLocalTable(); err != nil {
			return nil, err
		}
	}
	return store, nil
}

func (s *Store) ensureLocalTable() error {
	deadline := time.Now().Add(30 * time.Second)
	var last error
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := s.db.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: &s.table})
		cancel()
		if err == nil {
			return nil
		}
		var missing *types.ResourceNotFoundException
		if errors.As(err, &missing) {
			return s.createLocalTable()
		}
		last = err
		if time.Now().After(deadline) {
			return last
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (s *Store) createLocalTable() error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := s.db.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: &s.table,
		AttributeDefinitions: []types.AttributeDefinition{
			{AttributeName: aws.String("pk"), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String("sk"), AttributeType: types.ScalarAttributeTypeS},
		},
		KeySchema: []types.KeySchemaElement{
			{AttributeName: aws.String("pk"), KeyType: types.KeyTypeHash},
			{AttributeName: aws.String("sk"), KeyType: types.KeyTypeRange},
		},
		BillingMode: types.BillingModeProvisioned,
		ProvisionedThroughput: &types.ProvisionedThroughput{
			ReadCapacityUnits:  aws.Int64(5),
			WriteCapacityUnits: aws.Int64(5),
		},
	})
	var inUse *types.ResourceInUseException
	if err != nil && !errors.As(err, &inUse) {
		return err
	}
	return dynamodb.NewTableExistsWaiter(s.db).Wait(ctx, &dynamodb.DescribeTableInput{TableName: &s.table}, 15*time.Second)
}

func (s *Store) deleteTable() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := s.db.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: &s.table})
	var missing *types.ResourceNotFoundException
	if errors.As(err, &missing) {
		return nil
	}
	return err
}

func (s *Store) FindKey(token string) (storedKey, bool) {
	if token == "" {
		return storedKey{}, false
	}
	presented := hashToken(token)
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	item, ok, err := s.get(ctx, keyPK(presented), skActive)
	if err != nil {
		log.Printf("store: find key: %v", err)
		return storedKey{}, false
	}
	if !ok || subtle.ConstantTimeCompare([]byte(item.Hash), []byte(presented)) != 1 {
		return storedKey{}, false
	}
	return item.toKey(), true
}

func (s *Store) CreateKey(role, name string) (keyResponse, error) {
	token := role + "_" + randText(24)
	prefix := token
	if len(prefix) > 12 {
		prefix = prefix[:12]
	}
	record := storedKey{
		ID:        "key_" + randHex(6),
		Role:      role,
		Name:      name,
		Prefix:    prefix + "…",
		Hash:      hashToken(token),
		CreatedAt: now(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	err := s.transact(ctx,
		putNew(s.table, dbItem{PK: keyPK(record.Hash), SK: skActive, ID: record.ID, Role: record.Role, Name: record.Name, Prefix: record.Prefix, Hash: record.Hash, CreatedAt: record.CreatedAt}),
		putNew(s.table, dbItem{PK: keyIDPK(record.ID), SK: skActive, ID: record.ID, Hash: record.Hash}),
	)
	if err != nil {
		return keyResponse{}, s.fail("create key", err)
	}
	return keyResponse{ID: record.ID, Role: record.Role, Name: record.Name, Prefix: record.Prefix, CreatedAt: record.CreatedAt, Token: token}, nil
}

func (s *Store) ListKeys() []keyResponse {
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	items, err := s.scanPrefix(ctx, "key#")
	if err != nil {
		log.Printf("store: list keys: %v", err)
		return []keyResponse{}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt == items[j].CreatedAt {
			return items[i].ID < items[j].ID
		}
		return items[i].CreatedAt < items[j].CreatedAt
	})
	out := make([]keyResponse, 0, len(items))
	for _, item := range items {
		key := item.toKey()
		out = append(out, keyResponse{ID: key.ID, Role: key.Role, Name: key.Name, Prefix: key.Prefix, CreatedAt: key.CreatedAt})
	}
	return out
}

func (s *Store) DeleteKey(id string) (bool, error) {
	if id == "" {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	link, ok, err := s.get(ctx, keyIDPK(id), skActive)
	if err != nil {
		return false, s.fail("delete key", err)
	}
	if !ok || link.Hash == "" {
		return false, nil
	}
	_, err = s.db.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{Delete: &types.Delete{TableName: &s.table, Key: itemKey(keyPK(link.Hash), skActive)}},
			{Delete: &types.Delete{TableName: &s.table, Key: itemKey(keyIDPK(id), skActive)}},
		},
	})
	if err != nil {
		return false, s.fail("delete key", err)
	}
	return true, nil
}

func (s *Store) CreateRequest(userKeyID, osName, rawURL, token string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	var last error
	for range 3 {
		stamp := now()
		item := request{
			UID:       randText(9),
			OS:        osName,
			URL:       rawURL,
			Token:     token,
			UserKeyID: userKeyID,
			Status:    "queued",
			Result:    map[string]any{},
			CreatedAt: stamp,
			UpdatedAt: stamp,
		}
		err := s.putQueued(ctx, item)
		if err == nil {
			return item.UID, nil
		}
		if !conditionalCancel(err) {
			return "", s.fail("create request", err)
		}
		last = err
	}
	return "", s.fail("create request", last)
}

func (s *Store) GetForUser(userKeyID, uid string) (map[string]any, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	item, ok, err := s.get(ctx, reqPK(uid), skActive)
	if err != nil {
		log.Printf("store: get request: %v", err)
		return nil, false
	}
	if !ok || item.UserKeyID != userKeyID {
		return nil, false
	}
	req := item.toRequest()
	body := viewOf(&req)
	access, ok, err := s.get(ctx, accessPK(uid), skActive)
	if err != nil {
		log.Printf("store: get access: %v", err)
		return body, true
	}
	if ok {
		body["username"] = access.Username
		body["password"] = access.Password
	}
	return body, true
}

func (s *Store) DeleteForUser(userKeyID, uid string) (string, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	item, ok, err := s.get(ctx, reqPK(uid), skActive)
	if err != nil {
		return "", false, s.fail("delete request", err)
	}
	if !ok || item.UserKeyID != userKeyID {
		return "", false, nil
	}
	access, _, err := s.get(ctx, accessPK(uid), skActive)
	if err != nil {
		return "", false, s.fail("delete request", err)
	}
	actions := []types.TransactWriteItem{
		{Update: &types.Update{
			TableName:           &s.table,
			Key:                 itemKey(reqPK(uid), skActive),
			UpdateExpression:    aws.String("SET #status = :deleted, updatedAt = :now"),
			ConditionExpression: aws.String("#userKeyId = :kid AND attribute_exists(pk)"),
			ExpressionAttributeNames: map[string]string{
				"#status":    "status",
				"#userKeyId": "userKeyId",
			},
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":deleted": &types.AttributeValueMemberS{Value: "deleted"},
				":now":     &types.AttributeValueMemberS{Value: now()},
				":kid":     &types.AttributeValueMemberS{Value: userKeyID},
			},
		}},
		{Delete: &types.Delete{TableName: &s.table, Key: itemKey(accessPK(uid), skActive)}},
	}
	if access.Username != "" {
		actions = append(actions, types.TransactWriteItem{
			Delete: &types.Delete{TableName: &s.table, Key: itemKey(loginPK(access.Username), skActive)},
		})
	}
	if item.CreatedAt != "" && item.OS != "" {
		actions = append(actions,
			types.TransactWriteItem{Delete: &types.Delete{TableName: &s.table, Key: itemKey(queueOSPK(item.OS), queueOSSort(item.CreatedAt, uid))}},
			types.TransactWriteItem{Delete: &types.Delete{TableName: &s.table, Key: itemKey(pkQueue, queueAnySort(item.CreatedAt, item.OS, uid))}},
		)
	}
	_, err = s.db.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: actions})
	if err != nil {
		if conditionFailed(err) {
			return "", false, nil
		}
		return "", false, s.fail("delete request", err)
	}
	return uid, true, nil
}

func (s *Store) Dequeue(osName string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	fails := map[string]int{}
	for range claimTries {
		head, ok, err := s.queueHead(ctx, osName)
		if err != nil {
			return nil, s.fail("dequeue", err)
		}
		if !ok {
			return nil, nil
		}
		err = s.claim(ctx, head)
		if err == nil {
			item, found, getErr := s.get(ctx, reqPK(head.UID), skActive)
			if getErr != nil {
				return nil, s.fail("dequeue", getErr)
			}
			if !found {
				return nil, s.fail("dequeue", errors.New("claimed request missing"))
			}
			req := item.toRequest()
			return viewOf(&req), nil
		}
		if !conditionalCancel(err) {
			return nil, s.fail("dequeue", err)
		}
		key := head.PK + "\x00" + head.SK
		fails[key]++
		if fails[key] >= 2 {
			log.Printf("store: dropping queue row %s %s after claim conflict", head.PK, head.SK)
			s.dropQueue(ctx, head)
		}
	}
	return nil, s.fail("dequeue", errors.New("claim conflict"))
}

func (s *Store) Update(osName, uid string, body map[string]any) (map[string]any, error) {
	status := asString(body["status"])
	if status != "success" && status != "failure" {
		return nil, errStatus
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	item, ok, err := s.get(ctx, reqPK(uid), skActive)
	if err != nil {
		return nil, s.fail("update", err)
	}
	if !ok || item.OS != osName {
		return nil, errMissing
	}
	result := map[string]any{}
	maps.Copy(result, item.Result)
	for key, value := range body {
		if key == "__proto__" || key == "constructor" || key == "prototype" {
			continue
		}
		result[key] = value
	}
	encoded, err := jsonMap(result).MarshalDynamoDBAttributeValue()
	if err != nil {
		return nil, s.fail("update", err)
	}
	out, err := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           &s.table,
		Key:                 itemKey(reqPK(uid), skActive),
		UpdateExpression:    aws.String("SET #status = :status, updatedAt = :now, #result = :result"),
		ConditionExpression: aws.String("#os = :os AND attribute_exists(pk)"),
		ExpressionAttributeNames: map[string]string{
			"#status": "status",
			"#result": "result",
			"#os":     "os",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":status": &types.AttributeValueMemberS{Value: status},
			":now":    &types.AttributeValueMemberS{Value: now()},
			":result": encoded,
			":os":     &types.AttributeValueMemberS{Value: osName},
		},
		ReturnValues: types.ReturnValueAllNew,
	})
	if conditionFailed(err) {
		return nil, errMissing
	}
	if err != nil {
		return nil, s.fail("update", err)
	}
	var saved dbItem
	if err := attributevalue.UnmarshalMap(out.Attributes, &saved); err != nil {
		return nil, s.fail("update", err)
	}
	req := saved.toRequest()
	return viewOf(&req), nil
}

func (s *Store) SaveAccess(userKeyID, uid string, fields accessRow) (accessRow, bool, error) {
	fields.UID = uid
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	req, ok, err := s.get(ctx, reqPK(uid), skActive)
	if err != nil {
		return accessRow{}, false, s.fail("save access", err)
	}
	if !ok || req.UserKeyID != userKeyID || req.Status == "deleted" {
		return accessRow{}, false, errMissing
	}
	_, ok, err = s.get(ctx, accessPK(uid), skActive)
	if err != nil {
		return accessRow{}, false, s.fail("save access", err)
	}
	if ok {
		row, err := s.updateAccess(ctx, fields)
		return row, false, err
	}
	var last error
	for range 5 {
		stamp := now()
		row := accessRow{
			UID:       uid,
			Username:  "vm" + randText(6),
			Password:  randText(12),
			CreatedAt: stamp,
			URL:       fields.URL,
			Token:     fields.Token,
			RDPUser:   fields.RDPUser,
			RDPPass:   fields.RDPPass,
			Key:       fields.Key,
			UpdatedAt: stamp,
		}
		err := s.transact(ctx, putNew(s.table, accessItem(row)), putNew(s.table, loginItem(row)))
		if err == nil {
			return row, true, nil
		}
		canceled, codes := cancelCodes(err)
		if !canceled {
			return accessRow{}, false, s.fail("save access", err)
		}
		last = err
		if len(codes) > 0 && codes[0] == "ConditionalCheckFailed" {
			row, err := s.updateAccess(ctx, fields)
			return row, false, err
		}
	}
	return accessRow{}, false, s.fail("save access", last)
}

func (s *Store) GetAccess(uid string) *accessRow {
	if uid == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	item, ok, err := s.get(ctx, accessPK(uid), skActive)
	if err != nil || !ok {
		if err != nil {
			log.Printf("store: get access: %v", err)
		}
		return nil
	}
	if !s.open(ctx, uid) {
		return nil
	}
	row := item.toAccess()
	return &row
}

func (s *Store) Authenticate(username, password string) *accessRow {
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	item, ok, err := s.get(ctx, loginPK(username), skActive)
	if err != nil {
		log.Printf("store: authenticate: %v", err)
		return nil
	}
	storedUser, storedPass, uid := "vm00000000", "0000000000000000", ""
	if ok {
		storedUser, storedPass, uid = item.Username, item.Password, item.UID
	}
	if !passwordsMatch(username, password, storedUser, storedPass) || !ok || uid == "" {
		return nil
	}
	return s.GetAccess(uid)
}

func (s *Store) DeletedAmong(uids []string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	order := make([]string, 0, len(uids))
	seen := map[string]bool{}
	for _, uid := range uids {
		if uid == "" || seen[uid] {
			continue
		}
		seen[uid] = true
		order = append(order, uid)
	}
	pks := make([]string, len(order))
	for i, uid := range order {
		pks[i] = reqPK(uid)
	}
	found, err := s.batchGet(ctx, pks)
	if err != nil {
		log.Printf("store: deleted among: %v", err)
		return []string{}
	}
	out := []string{}
	for _, uid := range order {
		if found[reqPK(uid)].Status == "deleted" {
			out = append(out, uid)
		}
	}
	return out
}

func (s *Store) ListRequests() []queueRow {
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	items, err := s.scanPrefix(ctx, "req#")
	if err != nil {
		log.Printf("store: list requests: %v", err)
		return []queueRow{}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt == items[j].CreatedAt {
			return items[i].UID < items[j].UID
		}
		return items[i].CreatedAt < items[j].CreatedAt
	})
	var accessKeys []string
	for _, item := range items {
		if item.Status != "deleted" && item.UID != "" {
			accessKeys = append(accessKeys, accessPK(item.UID))
		}
	}
	access, err := s.batchGet(ctx, accessKeys)
	if err != nil {
		log.Printf("store: list access: %v", err)
		access = map[string]dbItem{}
	}
	out := make([]queueRow, 0, len(items))
	for i := range items {
		req := items[i].toRequest()
		row := queueRow{
			UID:       req.UID,
			OS:        req.OS,
			Status:    req.Status,
			CreatedAt: req.CreatedAt,
			UpdatedAt: req.UpdatedAt,
			Result:    viewOf(&req),
		}
		if req.Status != "deleted" {
			if viewer, ok := access[accessPK(req.UID)]; ok && viewer.Username != "" {
				row.Viewer = viewerPath(viewer.Username, viewer.Password)
			}
		}
		out = append(out, row)
	}
	return out
}

func (s *Store) SaveMicrovm(rec microvmRecord) (microvmRecord, error) {
	rec.ID = strings.TrimSpace(rec.ID)
	if !validMicrovmID(rec.ID) {
		return microvmRecord{}, errors.New("invalid microvm id")
	}
	if rec.Endpoint != "" {
		rec.URL = websocketURL(rec.Endpoint)
	}
	stamp := now()
	if rec.CreatedAt == "" {
		rec.CreatedAt = stamp
	}
	rec.UpdatedAt = stamp
	item := dbItem{
		PK:           microvmPK(rec.ID),
		SK:           skActive,
		MicrovmID:    rec.ID,
		Endpoint:     rec.Endpoint,
		URL:          rec.URL,
		Region:       rec.Region,
		State:        rec.State,
		StateReason:  rec.StateReason,
		ImageArn:     rec.ImageArn,
		ImageVersion: rec.ImageVersion,
		StartedAt:    rec.StartedAt,
		CreatedAt:    rec.CreatedAt,
		UpdatedAt:    rec.UpdatedAt,
		Token:        rec.Token,
		Key:          rec.Key,
		UID:          rec.UID,
	}
	av, err := attributevalue.MarshalMap(item)
	if err != nil {
		return microvmRecord{}, s.fail("save microvm", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	_, err = s.db.PutItem(ctx, &dynamodb.PutItemInput{TableName: &s.table, Item: av})
	if err != nil {
		return microvmRecord{}, s.fail("save microvm", err)
	}
	return rec, nil
}

func (s *Store) GetMicrovm(id string) (microvmRecord, bool, error) {
	if !validMicrovmID(id) {
		return microvmRecord{}, false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	item, ok, err := s.get(ctx, microvmPK(id), skActive)
	if err != nil {
		return microvmRecord{}, false, s.fail("get microvm", err)
	}
	if !ok {
		return microvmRecord{}, false, nil
	}
	return item.toMicrovm(), true, nil
}

func (s *Store) ListMicrovms() ([]microvmRecord, error) {
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	items, err := s.scanPrefix(ctx, "microvm#")
	if err != nil {
		return nil, s.fail("list microvms", err)
	}
	out := make([]microvmRecord, 0, len(items))
	for _, item := range items {
		if item.MicrovmID == "" {
			continue
		}
		out = append(out, item.toMicrovm())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt == out[j].CreatedAt {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt > out[j].CreatedAt
	})
	return out, nil
}

func (item dbItem) toMicrovm() microvmRecord {
	return microvmRecord{
		ID:           item.MicrovmID,
		Endpoint:     item.Endpoint,
		URL:          item.URL,
		Region:       item.Region,
		State:        item.State,
		StateReason:  item.StateReason,
		ImageArn:     item.ImageArn,
		ImageVersion: item.ImageVersion,
		StartedAt:    item.StartedAt,
		CreatedAt:    item.CreatedAt,
		UpdatedAt:    item.UpdatedAt,
		Token:        item.Token,
		Key:          item.Key,
		UID:          item.UID,
	}
}

func (s *Store) ImportFile(path string) (importCounts, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return importCounts{}, err
	}
	var file fileState
	if err := json.Unmarshal(body, &file); err != nil {
		return importCounts{}, err
	}
	var items []dbItem
	var counts importCounts
	for _, key := range file.Keys {
		if key.ID == "" || key.Hash == "" {
			continue
		}
		items = append(items,
			dbItem{PK: keyPK(key.Hash), SK: skActive, ID: key.ID, Role: key.Role, Name: key.Name, Prefix: key.Prefix, Hash: key.Hash, CreatedAt: key.CreatedAt},
			dbItem{PK: keyIDPK(key.ID), SK: skActive, ID: key.ID, Hash: key.Hash},
		)
		counts.Keys++
	}
	for _, req := range file.Requests {
		if req.UID == "" {
			continue
		}
		if req.CreatedAt == "" {
			req.CreatedAt = now()
		}
		if req.UpdatedAt == "" {
			req.UpdatedAt = req.CreatedAt
		}
		if req.Result == nil {
			req.Result = map[string]any{}
		}
		items = append(items, requestItem(req))
		counts.Requests++
		if req.Status == "queued" {
			items = append(items, queueItems(req)...)
			counts.Queued++
		}
	}
	for _, row := range file.Access {
		if row.UID == "" {
			continue
		}
		items = append(items, accessItem(row))
		if row.Username != "" {
			items = append(items, loginItem(row))
		}
		counts.Access++
	}
	if err := s.batchPut(items); err != nil {
		return importCounts{}, err
	}
	return counts, nil
}

func (s *Store) putQueued(ctx context.Context, item request) error {
	rows := append([]dbItem{requestItem(item)}, queueItems(item)...)
	puts := make([]types.TransactWriteItem, 0, len(rows))
	for _, row := range rows {
		puts = append(puts, putNew(s.table, row))
	}
	return s.transact(ctx, puts...)
}

func (s *Store) queueHead(ctx context.Context, osName string) (dbItem, bool, error) {
	pk := pkQueue
	if osName != "" {
		pk = queueOSPK(osName)
	}
	out, err := s.db.Query(ctx, &dynamodb.QueryInput{
		TableName:              &s.table,
		KeyConditionExpression: aws.String("pk = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": &types.AttributeValueMemberS{Value: pk},
		},
		ScanIndexForward: aws.Bool(true),
		Limit:            aws.Int32(1),
		ConsistentRead:   aws.Bool(true),
	})
	if err != nil {
		return dbItem{}, false, err
	}
	if len(out.Items) == 0 {
		return dbItem{}, false, nil
	}
	var item dbItem
	if err := attributevalue.UnmarshalMap(out.Items[0], &item); err != nil {
		return dbItem{}, false, err
	}
	return item, true, nil
}

func (s *Store) claim(ctx context.Context, head dbItem) error {
	_, err := s.db.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{Delete: &types.Delete{
				TableName:           &s.table,
				Key:                 itemKey(queueOSPK(head.OS), queueOSSort(head.CreatedAt, head.UID)),
				ConditionExpression: aws.String("attribute_exists(pk)"),
			}},
			{Delete: &types.Delete{
				TableName:           &s.table,
				Key:                 itemKey(pkQueue, queueAnySort(head.CreatedAt, head.OS, head.UID)),
				ConditionExpression: aws.String("attribute_exists(pk)"),
			}},
			{Update: &types.Update{
				TableName:           &s.table,
				Key:                 itemKey(reqPK(head.UID), skActive),
				UpdateExpression:    aws.String("SET #status = :pending, updatedAt = :now"),
				ConditionExpression: aws.String("#status = :queued"),
				ExpressionAttributeNames: map[string]string{
					"#status": "status",
				},
				ExpressionAttributeValues: map[string]types.AttributeValue{
					":pending": &types.AttributeValueMemberS{Value: "pending"},
					":queued":  &types.AttributeValueMemberS{Value: "queued"},
					":now":     &types.AttributeValueMemberS{Value: now()},
				},
			}},
		},
	})
	return err
}

func (s *Store) dropQueue(ctx context.Context, head dbItem) {
	keys := []map[string]types.AttributeValue{itemKey(head.PK, head.SK)}
	if head.OS != "" && head.UID != "" && head.CreatedAt != "" {
		keys = append(keys,
			itemKey(queueOSPK(head.OS), queueOSSort(head.CreatedAt, head.UID)),
			itemKey(pkQueue, queueAnySort(head.CreatedAt, head.OS, head.UID)),
		)
	}
	for _, key := range keys {
		_, _ = s.db.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: &s.table, Key: key})
	}
}

func (s *Store) updateAccess(ctx context.Context, fields accessRow) (accessRow, error) {
	out, err := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:        &s.table,
		Key:              itemKey(accessPK(fields.UID), skActive),
		UpdateExpression: aws.String("SET #url = :url, #token = :token, #rdpUser = :rdpUser, #rdpPass = :rdpPass, #k = :key, #updated = :now"),
		ExpressionAttributeNames: map[string]string{
			"#url":     "url",
			"#token":   "token",
			"#rdpUser": "rdpUsername",
			"#rdpPass": "rdpPassword",
			"#k":       "key",
			"#updated": "updatedAt",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":url":     &types.AttributeValueMemberS{Value: fields.URL},
			":token":   &types.AttributeValueMemberS{Value: fields.Token},
			":rdpUser": &types.AttributeValueMemberS{Value: fields.RDPUser},
			":rdpPass": &types.AttributeValueMemberS{Value: fields.RDPPass},
			":key":     &types.AttributeValueMemberS{Value: fields.Key},
			":now":     &types.AttributeValueMemberS{Value: now()},
		},
		ConditionExpression: aws.String("attribute_exists(pk)"),
		ReturnValues:        types.ReturnValueAllNew,
	})
	if err != nil {
		return accessRow{}, s.fail("save access", err)
	}
	var saved dbItem
	if err := attributevalue.UnmarshalMap(out.Attributes, &saved); err != nil {
		return accessRow{}, s.fail("save access", err)
	}
	return saved.toAccess(), nil
}

func (s *Store) open(ctx context.Context, uid string) bool {
	item, ok, err := s.get(ctx, reqPK(uid), skActive)
	if err != nil {
		log.Printf("store: open: %v", err)
		return false
	}
	return ok && item.Status != "deleted"
}

func (s *Store) get(ctx context.Context, pk, sk string) (dbItem, bool, error) {
	out, err := s.db.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      &s.table,
		Key:            itemKey(pk, sk),
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return dbItem{}, false, err
	}
	if len(out.Item) == 0 {
		return dbItem{}, false, nil
	}
	var item dbItem
	if err := attributevalue.UnmarshalMap(out.Item, &item); err != nil {
		return dbItem{}, false, err
	}
	return item, true, nil
}

func (s *Store) scanPrefix(ctx context.Context, prefix string) ([]dbItem, error) {
	var out []dbItem
	var start map[string]types.AttributeValue
	for {
		page, err := s.db.Scan(ctx, &dynamodb.ScanInput{
			TableName:        &s.table,
			FilterExpression: aws.String("begins_with(pk, :prefix) AND sk = :sk"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":prefix": &types.AttributeValueMemberS{Value: prefix},
				":sk":     &types.AttributeValueMemberS{Value: skActive},
			},
			ExclusiveStartKey: start,
			ConsistentRead:    aws.Bool(true),
		})
		if err != nil {
			return nil, err
		}
		var batch []dbItem
		if err := attributevalue.UnmarshalListOfMaps(page.Items, &batch); err != nil {
			return nil, err
		}
		out = append(out, batch...)
		if len(page.LastEvaluatedKey) == 0 {
			return out, nil
		}
		start = page.LastEvaluatedKey
	}
}

func (s *Store) batchGet(ctx context.Context, pks []string) (map[string]dbItem, error) {
	found := map[string]dbItem{}
	if len(pks) == 0 {
		return found, nil
	}
	unique := make([]string, 0, len(pks))
	seen := map[string]bool{}
	for _, pk := range pks {
		if pk == "" || seen[pk] {
			continue
		}
		seen[pk] = true
		unique = append(unique, pk)
	}
	for len(unique) > 0 {
		n := min(len(unique), 100)
		chunk := unique[:n]
		unique = unique[n:]
		pending := make([]map[string]types.AttributeValue, 0, len(chunk))
		for _, pk := range chunk {
			pending = append(pending, itemKey(pk, skActive))
		}
		for attempt := 0; attempt < 5 && len(pending) > 0; attempt++ {
			if attempt > 0 {
				time.Sleep(time.Duration(attempt*20) * time.Millisecond)
			}
			resp, err := s.db.BatchGetItem(ctx, &dynamodb.BatchGetItemInput{
				RequestItems: map[string]types.KeysAndAttributes{
					s.table: {Keys: pending, ConsistentRead: aws.Bool(true)},
				},
			})
			if err != nil {
				return nil, err
			}
			var batch []dbItem
			if err := attributevalue.UnmarshalListOfMaps(resp.Responses[s.table], &batch); err != nil {
				return nil, err
			}
			for _, item := range batch {
				found[item.PK] = item
			}
			pending = nil
			if left, ok := resp.UnprocessedKeys[s.table]; ok {
				pending = left.Keys
			}
		}
		if len(pending) > 0 {
			return nil, errors.New("batch get incomplete")
		}
	}
	return found, nil
}

func (s *Store) batchPut(items []dbItem) error {
	seen := map[string]dbItem{}
	order := make([]string, 0, len(items))
	for _, item := range items {
		id := item.PK + "\x00" + item.SK
		if _, ok := seen[id]; !ok {
			order = append(order, id)
		}
		seen[id] = item
	}
	var batch []types.WriteRequest
	flush := func() error {
		pending := batch
		batch = nil
		for attempt := 0; attempt < 6 && len(pending) > 0; attempt++ {
			if attempt > 0 {
				time.Sleep(time.Duration(attempt*30) * time.Millisecond)
			}
			ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
			out, err := s.db.BatchWriteItem(ctx, &dynamodb.BatchWriteItemInput{
				RequestItems: map[string][]types.WriteRequest{s.table: pending},
			})
			cancel()
			if err != nil {
				return err
			}
			pending = out.UnprocessedItems[s.table]
		}
		if len(pending) > 0 {
			return errors.New("import write incomplete")
		}
		return nil
	}
	for _, id := range order {
		av, err := attributevalue.MarshalMap(seen[id])
		if err != nil {
			return err
		}
		batch = append(batch, types.WriteRequest{PutRequest: &types.PutRequest{Item: av}})
		if len(batch) == 25 {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if len(batch) == 0 {
		return nil
	}
	return flush()
}

func (s *Store) transact(ctx context.Context, items ...types.TransactWriteItem) error {
	for i := range items {
		if items[i].Put == nil {
			return errors.New("empty transaction item")
		}
	}
	_, err := s.db.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items})
	return err
}

func (s *Store) fail(op string, err error) error {
	if err != nil {
		log.Printf("store: %s: %v", op, err)
	}
	return err
}

func putNew(table string, item dbItem) types.TransactWriteItem {
	av, err := attributevalue.MarshalMap(item)
	if err != nil {
		return types.TransactWriteItem{}
	}
	return types.TransactWriteItem{Put: &types.Put{
		TableName:           &table,
		Item:                av,
		ConditionExpression: aws.String("attribute_not_exists(pk)"),
	}}
}

func requestItem(item request) dbItem {
	return dbItem{
		PK:        reqPK(item.UID),
		SK:        skActive,
		UID:       item.UID,
		OS:        item.OS,
		URL:       item.URL,
		Token:     item.Token,
		UserKeyID: item.UserKeyID,
		Status:    item.Status,
		Result:    jsonMap(item.Result),
		CreatedAt: item.CreatedAt,
		UpdatedAt: item.UpdatedAt,
	}
}

func queueItems(item request) []dbItem {
	base := dbItem{UID: item.UID, OS: item.OS, CreatedAt: item.CreatedAt}
	osRow := base
	osRow.PK = queueOSPK(item.OS)
	osRow.SK = queueOSSort(item.CreatedAt, item.UID)
	anyRow := base
	anyRow.PK = pkQueue
	anyRow.SK = queueAnySort(item.CreatedAt, item.OS, item.UID)
	return []dbItem{osRow, anyRow}
}

func accessItem(row accessRow) dbItem {
	return dbItem{
		PK:        accessPK(row.UID),
		SK:        skActive,
		UID:       row.UID,
		Username:  row.Username,
		Password:  row.Password,
		CreatedAt: row.CreatedAt,
		URL:       row.URL,
		Token:     row.Token,
		RDPUser:   row.RDPUser,
		RDPPass:   row.RDPPass,
		Key:       row.Key,
		UpdatedAt: row.UpdatedAt,
	}
}

func loginItem(row accessRow) dbItem {
	return dbItem{
		PK:       loginPK(row.Username),
		SK:       skActive,
		UID:      row.UID,
		Username: row.Username,
		Password: row.Password,
	}
}

func (it dbItem) toKey() storedKey {
	return storedKey{ID: it.ID, Role: it.Role, Name: it.Name, Prefix: it.Prefix, Hash: it.Hash, CreatedAt: it.CreatedAt}
}

func (it dbItem) toRequest() request {
	result := map[string]any{}
	maps.Copy(result, it.Result)
	return request{
		UID:       it.UID,
		OS:        it.OS,
		URL:       it.URL,
		Token:     it.Token,
		UserKeyID: it.UserKeyID,
		Status:    it.Status,
		Result:    result,
		CreatedAt: it.CreatedAt,
		UpdatedAt: it.UpdatedAt,
	}
}

func (it dbItem) toAccess() accessRow {
	return accessRow{
		UID:       it.UID,
		Username:  it.Username,
		Password:  it.Password,
		CreatedAt: it.CreatedAt,
		URL:       it.URL,
		Token:     it.Token,
		RDPUser:   it.RDPUser,
		RDPPass:   it.RDPPass,
		Key:       it.Key,
		UpdatedAt: it.UpdatedAt,
	}
}

func viewerPath(username, password string) string {
	if username == "" {
		return ""
	}
	return "/microvm?username=" + url.QueryEscape(username) + "&password=" + url.QueryEscape(password)
}

func viewOf(item *request) map[string]any {
	out := map[string]any{}
	maps.Copy(out, item.Result)
	out["uid"] = item.UID
	out["os"] = item.OS
	out["url"] = item.URL
	out["token"] = item.Token
	out["status"] = item.Status
	if item.URL == "" {
		delete(out, "url")
	}
	if item.Token == "" {
		delete(out, "token")
	}
	return out
}

func itemKey(pk, sk string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: pk},
		"sk": &types.AttributeValueMemberS{Value: sk},
	}
}

func keyPK(hash string) string   { return "key#" + hash }
func keyIDPK(id string) string   { return "keyid#" + id }
func reqPK(uid string) string    { return "req#" + uid }
func microvmPK(id string) string { return "microvm#" + id }
func accessPK(uid string) string { return "access#" + uid }
func loginPK(name string) string { return "login#" + name }
func queueOSPK(osName string) string {
	return "queue#" + osName
}
func queueOSSort(createdAt, uid string) string { return createdAt + "#" + uid }
func queueAnySort(createdAt, osName, uid string) string {
	return createdAt + "#" + osName + "#" + uid
}

func conditionalCancel(err error) bool {
	_, codes := cancelCodes(err)
	if codes == nil {
		return false
	}
	for _, code := range codes {
		if code == "ConditionalCheckFailed" {
			return true
		}
	}
	return false
}

func cancelCodes(err error) (bool, []string) {
	var canceled *types.TransactionCanceledException
	if !errors.As(err, &canceled) {
		return false, nil
	}
	codes := make([]string, len(canceled.CancellationReasons))
	for i, reason := range canceled.CancellationReasons {
		if reason.Code != nil {
			codes[i] = *reason.Code
		}
	}
	return true, codes
}

func conditionFailed(err error) bool {
	var failed *types.ConditionalCheckFailedException
	return errors.As(err, &failed)
}

func passwordsMatch(username, password, storedUser, storedPass string) bool {
	userHash := sha256.Sum256([]byte(username))
	passHash := sha256.Sum256([]byte(password))
	uh := sha256.Sum256([]byte(storedUser))
	ph := sha256.Sum256([]byte(storedPass))
	userOK := subtle.ConstantTimeCompare(userHash[:], uh[:]) == 1
	passOK := subtle.ConstantTimeCompare(passHash[:], ph[:]) == 1
	return userOK && passOK
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func randText(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

func randHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buf)
}

func now() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}
