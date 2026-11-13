CREATE TABLE IF NOT EXISTS scanner_state (
    id          TINYINT PRIMARY KEY DEFAULT 1,
    last_block  BIGINT NOT NULL DEFAULT 0,
    updated_at  DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    CONSTRAINT single_row CHECK (id = 1)
);

CREATE TABLE IF NOT EXISTS watched_addresses (
    id                       INT AUTO_INCREMENT PRIMARY KEY,
    network                  VARCHAR(32)  NOT NULL DEFAULT 'tron',
    address                  VARCHAR(128) NOT NULL,
    hex40                    CHAR(40),
    subscribed_at            DATETIME(6)  NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_seen_at             DATETIME(6),
    expires_at               DATETIME(6)  NOT NULL,
    active                   TINYINT(1)   NOT NULL DEFAULT 1,
    disable_webhooks         TINYINT(1)   NOT NULL DEFAULT 0,
    meta                     JSON,
    blockchair_request_count INT          NOT NULL DEFAULT 0,
    UNIQUE KEY uq_network_address (network, address),
    INDEX idx_hex40               (hex40),
    INDEX idx_active_expires      (active, expires_at)
);

CREATE TABLE IF NOT EXISTS address_webhooks (
    id                  INT AUTO_INCREMENT PRIMARY KEY,
    watched_address_id  INT         NOT NULL,
    url                 TEXT        NOT NULL,
    secret              TEXT,
    timeout_sec         SMALLINT    NOT NULL DEFAULT 10,
    max_retries         SMALLINT    NOT NULL DEFAULT 3,
    active              TINYINT(1)  NOT NULL DEFAULT 1,
    created_at          DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    CONSTRAINT fk_aw_watched
        FOREIGN KEY (watched_address_id) REFERENCES watched_addresses(id)
        ON DELETE CASCADE,
    INDEX idx_aw_address_active (watched_address_id, active)
);

CREATE TABLE IF NOT EXISTS transfer_events (
    id                  BIGINT AUTO_INCREMENT PRIMARY KEY,
    network             VARCHAR(32)     NOT NULL DEFAULT 'tron',
    txid                VARCHAR(128)    NOT NULL,
    log_index           SMALLINT        NOT NULL DEFAULT 0,
    block_num           BIGINT,
    ts                  DATETIME(6)     NOT NULL,
    currency            VARCHAR(32)     NOT NULL,
    from_address        VARCHAR(128)    NOT NULL,
    to_address          VARCHAR(128)    NOT NULL,
    from_hex40          CHAR(40),
    to_hex40            CHAR(40),
    amount_raw          DECIMAL(38,0),
    amount              DECIMAL(28,6)   NOT NULL,
    fee                 DECIMAL(28,6),
    fee_currency        VARCHAR(32),
    direction           VARCHAR(4)      NOT NULL,
    watched_address_id  INT,
    created_at          DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    UNIQUE KEY uq_network_txid_log (network, txid, log_index),
    CONSTRAINT fk_te_watched
        FOREIGN KEY (watched_address_id) REFERENCES watched_addresses(id)
        ON DELETE SET NULL,
    INDEX idx_te_address_ts        (watched_address_id, ts DESC),
    INDEX idx_te_address_currency  (watched_address_id, currency),
    INDEX idx_te_address_direction (watched_address_id, direction),
    INDEX idx_te_network_ts        (network, ts DESC),
    INDEX idx_te_block             (block_num)
);

CREATE TABLE IF NOT EXISTS webhook_deliveries (
    id               BIGINT AUTO_INCREMENT PRIMARY KEY,
    event_id         BIGINT       NOT NULL,
    webhook_name     VARCHAR(255) NOT NULL,
    webhook_url      TEXT         NOT NULL,
    status           VARCHAR(16)  NOT NULL DEFAULT 'PENDING',
    attempts         SMALLINT     NOT NULL DEFAULT 0,
    last_attempt_at  DATETIME(6),
    next_retry_at    DATETIME(6),
    last_status_code SMALLINT,
    last_error       TEXT,
    created_at       DATETIME(6)  NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    CONSTRAINT fk_wd_event
        FOREIGN KEY (event_id) REFERENCES transfer_events(id)
        ON DELETE CASCADE,
    INDEX idx_wd_pending_retry (status, next_retry_at),
    INDEX idx_wd_event         (event_id)
);
