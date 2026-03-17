package kafka

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/IBM/sarama"

	"github.com/cloudreve/Cloudreve/v4/pkg/conf"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
)

var (
	// ErrDisabled 表示当前实例未启用 Kafka。
	ErrDisabled = errors.New("kafka is disabled")
	// ErrClosed 表示 Kafka 客户端已经关闭。
	ErrClosed = errors.New("kafka client already closed")
)

// Message 是 Cloudreve 内部统一使用的 Kafka 消息模型。
// 这里不直接把 sarama.ConsumerMessage 暴露到上层，目的是减少业务侧对底层客户端的耦合。
type Message struct {
	Topic     string
	Key       []byte
	Value     []byte
	Headers   map[string][]byte
	Partition int32
	Offset    int64
	Timestamp time.Time
}

// PublishResult 表示消息写入 Kafka 后 broker 返回的位点信息。
type PublishResult struct {
	Partition int32
	Offset    int64
}

// Handler 是单条消息处理函数。
// 返回 nil 表示消费成功并提交 offset；返回 error 表示本条消息保留给后续重试。
type Handler func(ctx context.Context, msg *Message) error

// ConsumerRegistration 定义一组 consumer group 订阅。
// 同一个 group 在多个 Cloudreve 实例上同时启动时，Kafka 会自动做分区再均衡，这就是第一层高可用。
// 同一个实例内部如果设置了 Concurrency > 1，则会以同一个 group id 创建多个本地 consumer group member。
type ConsumerRegistration struct {
	Name        string
	Group       string
	Topics      []string
	Concurrency int
	Handler     Handler
}

// Producer 封装 Cloudreve 内部使用的 Kafka 生产能力。
type Producer interface {
	Publish(ctx context.Context, msg *Message) (*PublishResult, error)
	PublishJSON(ctx context.Context, topic string, key []byte, payload any, headers map[string]string) (*PublishResult, error)
}

// Client 是 Cloudreve 内部使用的 Kafka 客户端。
type Client interface {
	Enabled() bool
	Producer() Producer
	RegisterConsumer(reg ConsumerRegistration) error
	Start(ctx context.Context) error
	Close() error
}

type disabledClient struct{}

func (d *disabledClient) Enabled() bool { return false }
func (d *disabledClient) Producer() Producer {
	return &disabledProducer{}
}
func (d *disabledClient) RegisterConsumer(reg ConsumerRegistration) error { return ErrDisabled }
func (d *disabledClient) Start(ctx context.Context) error                 { return nil }
func (d *disabledClient) Close() error                                    { return nil }

type disabledProducer struct{}

func (d *disabledProducer) Publish(ctx context.Context, msg *Message) (*PublishResult, error) {
	return nil, ErrDisabled
}

func (d *disabledProducer) PublishJSON(ctx context.Context, topic string, key []byte, payload any, headers map[string]string) (*PublishResult, error) {
	return nil, ErrDisabled
}

type client struct {
	logger    logging.Logger
	cfg       *conf.Kafka
	brokers   []string
	saramaCfg *sarama.Config
	producer  sarama.SyncProducer
	publisher Producer

	mu            sync.Mutex
	started       bool
	closed        bool
	consumeCtx    context.Context
	cancel        context.CancelFunc
	registrations []ConsumerRegistration
	consumerGroup []sarama.ConsumerGroup
	wg            sync.WaitGroup
}

type producer struct {
	p   sarama.SyncProducer
	log logging.Logger
}

// New 创建一个 Kafka 客户端。
// 如果配置未启用，则返回 no-op 实现，调用方不需要在业务层反复做 nil 判空。
func New(cfg *conf.Kafka, logger logging.Logger) (Client, error) {
	if cfg == nil || !cfg.Enabled {
		return &disabledClient{}, nil
	}

	brokers := splitAndTrim(cfg.Brokers)
	if len(brokers) == 0 {
		return nil, fmt.Errorf("kafka brokers is empty")
	}

	saramaCfg, err := buildSaramaConfig(cfg)
	if err != nil {
		return nil, err
	}

	syncProducer, err := sarama.NewSyncProducer(brokers, saramaCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create kafka producer: %w", err)
	}

	p := &producer{
		p:   syncProducer,
		log: logger.CopyWithPrefix("[kafka-producer]"),
	}

	return &client{
		logger:    logger.CopyWithPrefix("[kafka]"),
		cfg:       cfg,
		brokers:   brokers,
		saramaCfg: saramaCfg,
		producer:  syncProducer,
		publisher: p,
	}, nil
}

func (c *client) Enabled() bool {
	return true
}

func (c *client) Producer() Producer {
	return c.publisher
}

func (c *client) RegisterConsumer(reg ConsumerRegistration) error {
	if err := validateRegistration(reg); err != nil {
		return err
	}

	normalized := ConsumerRegistration{
		Name:        reg.Name,
		Group:       reg.Group,
		Topics:      append([]string(nil), reg.Topics...),
		Concurrency: normalizeConcurrency(reg.Concurrency),
		Handler:     reg.Handler,
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return ErrClosed
	}

	c.registrations = append(c.registrations, normalized)
	if !c.started {
		return nil
	}

	// 允许在 Start 之后动态挂载消费者，便于 Cloudreve 业务模块按需延迟注册。
	if err := c.startRegistrationLocked(normalized); err != nil {
		c.registrations = c.registrations[:len(c.registrations)-1]
		return err
	}

	return nil
}

func (c *client) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return ErrClosed
	}
	if c.started {
		return nil
	}

	if ctx == nil {
		ctx = context.Background()
	}

	ctx, cancel := context.WithCancel(ctx)
	c.consumeCtx = ctx
	c.cancel = cancel

	for _, reg := range c.registrations {
		if err := c.startRegistrationLocked(reg); err != nil {
			cancel()
			for _, created := range c.consumerGroup {
				_ = created.Close()
			}
			c.consumerGroup = nil
			c.consumeCtx = nil
			c.cancel = nil
			return err
		}
	}

	c.started = true
	if len(c.registrations) > 0 {
		c.logger.Info("Kafka consumer started with %d registrations.", len(c.registrations))
	} else {
		c.logger.Info("Kafka producer is enabled, no consumer registration found.")
	}
	return nil
}

func (c *client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true

	cancel := c.cancel
	groups := append([]sarama.ConsumerGroup(nil), c.consumerGroup...)
	c.consumerGroup = nil
	c.consumeCtx = nil
	producer := c.producer
	c.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	var closeErr error
	for _, group := range groups {
		if err := group.Close(); err != nil && closeErr == nil {
			closeErr = fmt.Errorf("failed to close kafka consumer group: %w", err)
		}
	}

	c.wg.Wait()

	if producer != nil {
		if err := producer.Close(); err != nil && closeErr == nil {
			closeErr = fmt.Errorf("failed to close kafka producer: %w", err)
		}
	}

	return closeErr
}

func (c *client) startRegistrationLocked(reg ConsumerRegistration) error {
	if c.consumeCtx == nil {
		return fmt.Errorf("kafka client is not started")
	}

	startIndex := len(c.consumerGroup)
	workerCount := normalizeConcurrency(reg.Concurrency)
	for worker := 0; worker < workerCount; worker++ {
		group, err := sarama.NewConsumerGroup(c.brokers, reg.Group, c.saramaCfg)
		if err != nil {
			for _, created := range c.consumerGroup[startIndex:] {
				_ = created.Close()
			}
			c.consumerGroup = c.consumerGroup[:startIndex]
			return fmt.Errorf("failed to create kafka consumer group %q: %w", reg.Group, err)
		}

		c.consumerGroup = append(c.consumerGroup, group)
		c.wg.Add(1)
		go c.consumeLoop(c.consumeCtx, reg, group, worker)

		if c.saramaCfg.Consumer.Return.Errors {
			c.wg.Add(1)
			go c.logConsumerErrors(c.consumeCtx, reg, group, worker)
		}
	}

	c.logger.Info("Kafka consumer registered, name=%s group=%s topics=%v workers=%d", reg.Name, reg.Group, reg.Topics, workerCount)
	return nil
}

func (c *client) consumeLoop(ctx context.Context, reg ConsumerRegistration, group sarama.ConsumerGroup, worker int) {
	defer c.wg.Done()

	handler := &consumerGroupHandler{
		registration: reg,
		log:          c.logger.CopyWithPrefix(fmt.Sprintf("[consumer:%s#%d]", reg.Name, worker)),
	}

	for {
		if ctx.Err() != nil {
			return
		}

		if err := group.Consume(ctx, reg.Topics, handler); err != nil {
			if ctx.Err() != nil || errors.Is(err, sarama.ErrClosedConsumerGroup) {
				return
			}

			handler.log.Warning("Kafka consume loop failed: %s", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
	}
}

func (c *client) logConsumerErrors(ctx context.Context, reg ConsumerRegistration, group sarama.ConsumerGroup, worker int) {
	defer c.wg.Done()

	logger := c.logger.CopyWithPrefix(fmt.Sprintf("[consumer:%s#%d]", reg.Name, worker))
	for {
		select {
		case <-ctx.Done():
			return
		case err, ok := <-group.Errors():
			if !ok {
				return
			}
			if err != nil {
				logger.Warning("Kafka consumer async error: %s", err)
			}
		}
	}
}

func (p *producer) Publish(ctx context.Context, msg *Message) (*PublishResult, error) {
	if ctx != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	}

	if msg == nil {
		return nil, fmt.Errorf("kafka message is nil")
	}
	if strings.TrimSpace(msg.Topic) == "" {
		return nil, fmt.Errorf("kafka topic is empty")
	}

	producerMsg := &sarama.ProducerMessage{
		Topic:   msg.Topic,
		Value:   sarama.ByteEncoder(msg.Value),
		Headers: toSaramaHeaders(msg.Headers),
	}
	if len(msg.Key) > 0 {
		producerMsg.Key = sarama.ByteEncoder(msg.Key)
	}
	if !msg.Timestamp.IsZero() {
		producerMsg.Timestamp = msg.Timestamp
	}

	partition, offset, err := p.p.SendMessage(producerMsg)
	if err != nil {
		return nil, err
	}

	return &PublishResult{
		Partition: partition,
		Offset:    offset,
	}, nil
}

func (p *producer) PublishJSON(ctx context.Context, topic string, key []byte, payload any, headers map[string]string) (*PublishResult, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal kafka json payload: %w", err)
	}

	rawHeaders := make(map[string][]byte, len(headers)+1)
	rawHeaders["content-type"] = []byte("application/json")
	for headerKey, headerValue := range headers {
		rawHeaders[headerKey] = []byte(headerValue)
	}

	return p.Publish(ctx, &Message{
		Topic:   topic,
		Key:     key,
		Value:   body,
		Headers: rawHeaders,
	})
}

type consumerGroupHandler struct {
	registration ConsumerRegistration
	log          logging.Logger
}

func (h *consumerGroupHandler) Setup(session sarama.ConsumerGroupSession) error {
	h.log.Info("Kafka consumer session started, claims=%v", session.Claims())
	return nil
}

func (h *consumerGroupHandler) Cleanup(session sarama.ConsumerGroupSession) error {
	h.log.Info("Kafka consumer session closed.")
	return nil
}

func (h *consumerGroupHandler) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	for {
		select {
		case <-session.Context().Done():
			return nil
		case msg, ok := <-claim.Messages():
			if !ok {
				return nil
			}

			err := safeHandleMessage(session.Context(), h.registration.Handler, &Message{
				Topic:     msg.Topic,
				Key:       msg.Key,
				Value:     msg.Value,
				Headers:   fromSaramaHeaders(msg.Headers),
				Partition: msg.Partition,
				Offset:    msg.Offset,
				Timestamp: msg.Timestamp,
			})
			if err != nil {
				h.log.Warning("Kafka message handle failed, topic=%s partition=%d offset=%d err=%s", msg.Topic, msg.Partition, msg.Offset, err)
				continue
			}

			session.MarkMessage(msg, "")
		}
	}
}

func safeHandleMessage(ctx context.Context, handler Handler, msg *Message) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("panic in kafka handler: %v", recovered)
		}
	}()

	return handler(ctx, msg)
}

func buildSaramaConfig(cfg *conf.Kafka) (*sarama.Config, error) {
	version, err := sarama.ParseKafkaVersion(strings.TrimSpace(cfg.Version))
	if err != nil {
		return nil, fmt.Errorf("failed to parse kafka version %q: %w", cfg.Version, err)
	}

	securityProtocol, err := resolveSecurityProtocol(cfg)
	if err != nil {
		return nil, err
	}

	config := sarama.NewConfig()
	config.Version = version
	config.ClientID = strings.TrimSpace(cfg.ClientID)
	config.Metadata.Full = true
	config.Metadata.Retry.Max = maxInt(cfg.Producer.RetryMax, 3)
	config.Metadata.Retry.Backoff = time.Duration(maxInt(cfg.Consumer.RetryBackoff, cfg.Producer.RetryBackoff)) * time.Second
	config.Net.DialTimeout = time.Duration(cfg.DialTimeout) * time.Second
	config.Net.ReadTimeout = time.Duration(cfg.ReadTimeout) * time.Second
	config.Net.WriteTimeout = time.Duration(cfg.WriteTimeout) * time.Second
	config.Net.KeepAlive = time.Duration(cfg.KeepAlive) * time.Second

	// 幂等生产者要求 Return.Successes=true、WaitForAll 和有限的并发请求数。
	config.Producer.RequiredAcks = requiredAcksFromString(cfg.Producer.RequiredAcks)
	config.Producer.Compression = compressionFromString(cfg.Producer.Compression)
	config.Producer.Retry.Max = cfg.Producer.RetryMax
	config.Producer.Retry.Backoff = time.Duration(cfg.Producer.RetryBackoff) * time.Second
	config.Producer.Idempotent = cfg.Producer.Idempotent
	config.Producer.Return.Successes = cfg.Producer.ReturnSuccesses
	config.Producer.Return.Errors = true
	if cfg.Producer.MaxMessageBytes > 0 {
		config.Producer.MaxMessageBytes = cfg.Producer.MaxMessageBytes
	}
	if cfg.Producer.Idempotent {
		config.Net.MaxOpenRequests = 1
		config.Producer.RequiredAcks = sarama.WaitForAll
		config.Producer.Return.Successes = true
		if config.Producer.Retry.Max == 0 {
			config.Producer.Retry.Max = 3
		}
	}

	config.Consumer.Return.Errors = cfg.Consumer.ReturnErrors
	config.Consumer.Offsets.Initial = initialOffsetFromString(cfg.Consumer.InitialOffset)
	config.Consumer.MaxProcessingTime = time.Duration(cfg.Consumer.MaxProcessingTime) * time.Second
	if cfg.Consumer.FetchDefault > 0 {
		config.Consumer.Fetch.Default = int32(cfg.Consumer.FetchDefault)
	}
	if cfg.Consumer.FetchMax > 0 {
		config.Consumer.Fetch.Max = int32(cfg.Consumer.FetchMax)
	}
	config.Consumer.Group.Session.Timeout = time.Duration(cfg.Consumer.SessionTimeout) * time.Second
	config.Consumer.Group.Heartbeat.Interval = time.Duration(cfg.Consumer.HeartbeatInterval) * time.Second
	config.Consumer.Group.Rebalance.Strategy = rebalanceStrategyFromString(cfg.Consumer.RebalanceStrategy)
	config.Consumer.Group.Rebalance.Retry.Backoff = time.Duration(cfg.Consumer.RetryBackoff) * time.Second

	if securityProtocol.useTLS() {
		config.Net.TLS.Enable = true
		config.Net.TLS.Config, err = buildTLSConfig(cfg)
		if err != nil {
			return nil, err
		}
	}

	if securityProtocol.useSASL() {
		mechanism := strings.ToUpper(strings.TrimSpace(cfg.SASLMechanism))
		if mechanism == "" {
			mechanism = "PLAIN"
		}
		if mechanism != "PLAIN" {
			return nil, fmt.Errorf("unsupported kafka sasl mechanism %q", cfg.SASLMechanism)
		}
		if strings.TrimSpace(cfg.SASLUsername) == "" {
			return nil, fmt.Errorf("kafka sasl username is empty")
		}
		if strings.TrimSpace(cfg.SASLPassword) == "" {
			return nil, fmt.Errorf("kafka sasl password is empty")
		}

		config.Net.SASL.Enable = true
		config.Net.SASL.Handshake = cfg.SASLHandshake
		config.Net.SASL.Mechanism = sarama.SASLTypePlaintext
		config.Net.SASL.User = cfg.SASLUsername
		config.Net.SASL.Password = cfg.SASLPassword
	}

	return config, nil
}

type securityProtocol string

const (
	securityProtocolPlaintext securityProtocol = "PLAINTEXT"
	securityProtocolSSL       securityProtocol = "SSL"
	securityProtocolSASLPlain securityProtocol = "SASL_PLAINTEXT"
	securityProtocolSASLSSL   securityProtocol = "SASL_SSL"
)

func resolveSecurityProtocol(cfg *conf.Kafka) (securityProtocol, error) {
	if cfg == nil {
		return securityProtocolPlaintext, nil
	}

	protocol := strings.ToUpper(strings.TrimSpace(cfg.SecurityProtocol))
	switch securityProtocol(protocol) {
	case securityProtocolPlaintext, securityProtocolSSL, securityProtocolSASLPlain, securityProtocolSASLSSL:
		return securityProtocol(protocol), nil
	}

	if protocol != "" {
		return "", fmt.Errorf("unsupported kafka security protocol %q", cfg.SecurityProtocol)
	}

	// 兼容旧配置：SecurityProtocol 为空时，按历史 TLSEnabled / SASLEnabled 推导。
	switch {
	case cfg.TLSEnabled && cfg.SASLEnabled:
		return securityProtocolSASLSSL, nil
	case cfg.SASLEnabled:
		return securityProtocolSASLPlain, nil
	case cfg.TLSEnabled:
		return securityProtocolSSL, nil
	default:
		return securityProtocolPlaintext, nil
	}
}

func (p securityProtocol) useTLS() bool {
	return p == securityProtocolSSL || p == securityProtocolSASLSSL
}

func (p securityProtocol) useSASL() bool {
	return p == securityProtocolSASLPlain || p == securityProtocolSASLSSL
}

func buildTLSConfig(cfg *conf.Kafka) (*tls.Config, error) {
	tlsCfg := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: cfg.TLSSkipVerify,
	}

	if serverName := strings.TrimSpace(cfg.TLSServerName); serverName != "" {
		tlsCfg.ServerName = serverName
	}

	if caPath := strings.TrimSpace(cfg.TLSCAPath); caPath != "" {
		caPEM, err := os.ReadFile(caPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read kafka tls ca cert %q: %w", caPath, err)
		}

		rootCAs := x509.NewCertPool()
		if !rootCAs.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("failed to append kafka tls ca cert %q", caPath)
		}
		tlsCfg.RootCAs = rootCAs
	}

	certPath := strings.TrimSpace(cfg.TLSCertPath)
	keyPath := strings.TrimSpace(cfg.TLSKeyPath)
	if (certPath == "") != (keyPath == "") {
		return nil, fmt.Errorf("kafka tls cert and key must be configured together")
	}
	if certPath != "" {
		clientCert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load kafka tls client cert pair: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{clientCert}
	}

	return tlsCfg, nil
}

func validateRegistration(reg ConsumerRegistration) error {
	if strings.TrimSpace(reg.Name) == "" {
		return fmt.Errorf("consumer name is empty")
	}
	if strings.TrimSpace(reg.Group) == "" {
		return fmt.Errorf("consumer group is empty")
	}
	if len(reg.Topics) == 0 {
		return fmt.Errorf("consumer topics is empty")
	}
	if reg.Handler == nil {
		return fmt.Errorf("consumer handler is nil")
	}

	for _, topic := range reg.Topics {
		if strings.TrimSpace(topic) == "" {
			return fmt.Errorf("consumer topic contains empty item")
		}
	}
	return nil
}

func normalizeConcurrency(concurrency int) int {
	if concurrency <= 0 {
		return 1
	}
	return concurrency
}

func splitAndTrim(raw string) []string {
	parts := strings.Split(raw, ",")
	res := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			res = append(res, part)
		}
	}
	return res
}

func toSaramaHeaders(headers map[string][]byte) []sarama.RecordHeader {
	if len(headers) == 0 {
		return nil
	}

	res := make([]sarama.RecordHeader, 0, len(headers))
	for key, value := range headers {
		res = append(res, sarama.RecordHeader{
			Key:   []byte(key),
			Value: value,
		})
	}
	return res
}

func fromSaramaHeaders(headers []*sarama.RecordHeader) map[string][]byte {
	if len(headers) == 0 {
		return nil
	}

	res := make(map[string][]byte, len(headers))
	for _, header := range headers {
		if header == nil {
			continue
		}
		res[string(header.Key)] = header.Value
	}
	return res
}

func requiredAcksFromString(raw string) sarama.RequiredAcks {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "none":
		return sarama.NoResponse
	case "local":
		return sarama.WaitForLocal
	default:
		return sarama.WaitForAll
	}
}

func compressionFromString(raw string) sarama.CompressionCodec {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "gzip":
		return sarama.CompressionGZIP
	case "snappy":
		return sarama.CompressionSnappy
	case "lz4":
		return sarama.CompressionLZ4
	case "zstd":
		return sarama.CompressionZSTD
	default:
		return sarama.CompressionNone
	}
}

func initialOffsetFromString(raw string) int64 {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "oldest":
		return sarama.OffsetOldest
	default:
		return sarama.OffsetNewest
	}
}

func rebalanceStrategyFromString(raw string) sarama.BalanceStrategy {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "range":
		return sarama.NewBalanceStrategyRange()
	case "roundrobin":
		return sarama.NewBalanceStrategyRoundRobin()
	default:
		return sarama.NewBalanceStrategySticky()
	}
}

func maxInt(vals ...int) int {
	res := 0
	for _, val := range vals {
		if val > res {
			res = val
		}
	}
	return res
}
