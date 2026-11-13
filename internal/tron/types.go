package tron

// Block represents a Tron block response from the Full Node API.
type Block struct {
	BlockHeader  BlockHeader   `json:"block_header"`
	Transactions []Transaction `json:"transactions"`
}

// BlockHeader contains block metadata.
type BlockHeader struct {
	RawData BlockHeaderRawData `json:"raw_data"`
}

// BlockHeaderRawData holds raw block header fields.
type BlockHeaderRawData struct {
	Number    int64 `json:"number"`
	Timestamp int64 `json:"timestamp"` // milliseconds since epoch
}

// Transaction represents a Tron transaction.
type Transaction struct {
	TxID    string         `json:"txID"`
	RawData TransactionRaw `json:"raw_data"`
	Ret     []TxRet        `json:"ret"`
}

// TxRet holds the transaction result.
type TxRet struct {
	ContractRet string `json:"contractRet"`
}

// TransactionRaw holds the raw transaction data.
type TransactionRaw struct {
	Contract  []Contract `json:"contract"`
	Timestamp int64      `json:"timestamp"` // milliseconds since epoch
}

// Contract represents a Tron contract call within a transaction.
type Contract struct {
	Type      string        `json:"type"`
	Parameter ContractParam `json:"parameter"`
}

// ContractParam holds contract call parameters.
type ContractParam struct {
	Value ContractValue `json:"value"`
}

// ContractValue holds the value of a contract call.
// Used for TransferContract (TRX) and TriggerSmartContract (USDT).
type ContractValue struct {
	// For TransferContract
	OwnerAddress string `json:"owner_address"`
	ToAddress    string `json:"to_address"`
	Amount       int64  `json:"amount"`

	// For TriggerSmartContract
	ContractAddress string `json:"contract_address"`
	Data            string `json:"data"`
}

// TransactionInfo represents the transaction info response from the Full Node API.
type TransactionInfo struct {
	ID             string    `json:"id"`
	BlockNumber    int64     `json:"blockNumber"`
	BlockTimeStamp int64     `json:"blockTimeStamp"` // milliseconds
	Log            []TxLog   `json:"log"`
	Receipt        TxReceipt `json:"receipt"`
}

// TxLog represents a single event log entry from a smart contract.
type TxLog struct {
	Address string   `json:"address"`
	Topics  []string `json:"topics"`
	Data    string   `json:"data"`
}

// TxReceipt holds the transaction receipt.
type TxReceipt struct {
	Result string `json:"result"`
}

// NowBlockResponse is the response from /wallet/getnowblock.
type NowBlockResponse struct {
	BlockHeader BlockHeader `json:"block_header"`
}
