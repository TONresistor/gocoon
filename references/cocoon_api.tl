int ? = Int;
long ? = Long;
double ? = Double;
string ? = String;
object ? = Object;
function ? = Function;
bytes data:string = Bytes;
true = True;
boolTrue = Bool;
boolFalse = Bool;


vector {t:Type} # [ t ] = Vector t;

int128 4*[ int ] = Int128;
int256 8*[ int ] = Int256;


tcp.ping id:long = tcp.Packet;
tcp.pong id:long = tcp.Packet;
tcp.packet data:bytes = tcp.Packet;
tcp.queryAnswer id:long data:bytes = tcp.Packet;
tcp.queryError id:long code:int message:string = tcp.Packet;
tcp.query id:long data:bytes = tcp.Packet;
tcp.connected id:long = tcp.Packet;
tcp.connect id:long = tcp.Packet;

ton.blockIdExt workchain:int shard:long seqno:int root_hash:bytes file_hash:bytes = ton.BlockIdExt;

test.answer id:long = test.Answer;

---functions---

test.query id:long = test.Answer;

---types---

proxyRunner.config
  is_test:Bool
  is_testnet:Bool
  workers_rpc_port:int
  client_rpc_port:int
  http_port:int
  owner_address:string
  connect_to_proxy_via:string
  root_contract_address:string
  machine_specific_private_key:int256
  node_wallet_key:int256
  ton_config_filename:string
  db_path:string
  check_worker_hashes:Bool
  image_hash:int256
  http_access_hash:string = proxyRunner.Config;

workerRunner.config
  is_test:Bool
  is_testnet:Bool
  http_port:int
  owner_address:string
  proxy_connections:int
  connect_to_proxy_via:string
  model_name:string
  coefficient:int
  root_contract_address:string
  node_wallet_key:int256
  forward_requests_to:string
  ton_config_filename:string
  check_proxy_hashes:Bool
  max_active_requests:int
  image_hash:int256
  http_access_hash:string 
  change_model_script:string
  machine_description_json:string
  = workerRunner.Config;

clientRunner.config
  is_test:Bool
  is_testnet:Bool
  http_port:int
  owner_address:string
  proxy_connections:int
  connect_to_proxy_via:string
  root_contract_address:string
  node_wallet_key:int256
  check_proxy_hashes:Bool
  secret_string:string
  ton_config_filename:string
  http_access_hash:string = clientRunner.Config;

keyStorageRunner.config
  is_test:Bool
  is_testnet:Bool
  add_default_key:Bool
  rpc_port:int
  http_port:int
  owner_address:string
  root_contract_address:string
  machine_specific_private_key:int256
  node_wallet_key:int256
  ton_config_filename:string
  db_path:string
  check_hashes:Bool
  image_hash:int256
  http_access_hash:string = keyStorageRunner.Config;

rootConfig.registeredProxy seqno:int address:string = rootConfig.RegisteredProxy;
rootConfig.registeredPublicKey public_key:int256 key_type:int expire_at:int = rootConfig.RegisteredPublicKey;

rootConfig.pseudo proxy_hashes:(vector int256) worker_hashes:(vector int256) model_hashes:(vector int256) model_types:(vector string)
                    registered_proxies:(vector rootConfig.registeredProxy)
                    price_per_token:int
                    worker_fee_per_token:int
                    last_proxy_seqno:int
                    version:int
                    params_version:int
                    registered_public_keys:(vector rootConfig.registeredPublicKey)
                    key_manager_address:string
                    proxy_sc_code:string
                    worker_sc_code:string
                    client_sc_code:string
                    root_owner_address:string = rootConfig.Config;
rootConfig.configV7 root_owner_address:string proxy_hashes:(vector int256) registered_proxies:(vector rootConfig.registeredProxy)
                    last_proxy_seqno:int worker_hashes:(vector int256) model_hashes:(vector int256) version:int 
                    struct_version:int
                    params_version:int
                    unique_id:int
                    is_test:int
                    price_per_token:long
                    worker_fee_per_token:long
                    prompt_tokens_price_multiplier:int
                    cached_tokens_price_multiplier:int
                    completion_tokens_price_multiplier:int
                    reasoning_tokens_price_multiplier:int
                    proxy_delay_before_close:int
                    client_delay_before_close:int
                    min_proxy_stake:long
                    min_client_stake:long
                    registered_public_keys:(vector rootConfig.registeredPublicKey)
                    key_manager_public_key:int256
                    key_manager_image_hash:int256
                    key_manager_address:string
                    verified_proxy_keys:(vector int256)
                    verified_worker_keys:(vector int256)
                    verified_key_manager_keys:(vector int256)
                    proxy_sc_code:string
                    worker_sc_code:string
                    client_sc_code:string
                    = rootConfig.Config;

---functions---

---types---

proxyDb.clientInfo owner_address:string sc_address:string status:int balance:long sc_tokens_used:long tokens_used:long
                   secret_hash:int256 last_request_at:int = proxyDb.ClientInfo;
proxyDb.clientInfoV2 owner_address:string sc_address:string status:int balance:long stake:long sc_tokens_used:long tokens_used:long
                   secret_hash:int256 last_request_at:int = proxyDb.ClientInfo;
proxyDb.workerInfo owner_address:string sc_address:string sc_tokens:long tokens:long last_request_at:int = proxyDb.WorkerInfo;
proxyDb.pendingBlockchainSeqnoCommit seqno:int session_hash:int256 = proxyDb.PendingBlockchainSeqnoCommit;

proxyDb.configEmpty = proxyDb.Config;
proxyDb.configV4 root_contract_address:string is_testnet:int root_contract_state:rootConfig.Config root_contract_state_block_ts:int
  sc_block_id:ton.blockIdExt last_seqno_committed_to_blockchain:int pending_blockchain_seqno_commits:(vector proxyDb.pendingBlockchainSeqnoCommit) = proxyDb.Config;
proxyDb.configV4Disabled root_contract_address:string is_testnet:int root_contract_state:rootConfig.Config root_contract_state_block_ts:int
  disabled_until_version:long = proxyDb.Config;

proxyDb.oldClient owner_address:string tokens:long next_client:string = proxyDb.OldClient;
proxyDb.oldWorker owner_address:string tokens:long next_worker:string = proxyDb.OldWorker;
proxyDb.oldInstance contract_address:string closing_state:int close_at:int next_client:string next_worker:string root_contract_state:rootConfig.Config =
  proxyDb.OldInstance;


keyManagerDb.configEmpty = keyManagerDb.Config;
keyManagerDb.configV1 root_contract_version:int = keyManagerDb.Config;

keyManagerDb.key private_key:int256 key_type:int valid_since_config_version:int valid_since_utime:int
                 valid_until_utime:int = keyManagerDb.Key;

---functions---

---types---

tokensUsed prompt_tokens_used:long cached_tokens_used:long completion_tokens_used:long reasoning_tokens_used:long 
           total_tokens_used:long = TokensUsed;

worker.params#869c73ed flags:# 
  worker_owner_address:string 
  model:string 
  coefficient:int
  is_test:flags.0?Bool
  proxy_cnt:flags.0?int
  max_active_requests:flags.0?int
  min_proto_version:flags.1?int
  max_proto_version:flags.1?int
  machine_description_json:flags.2?string
  = worker.Params;

proxy.params#d5c5609f flags:# 
  proxy_public_key:int256 
  proxy_owner_address:string 
  proxy_sc_address:string
  is_test:flags.0?Bool
  proto_version:flags.1?int
  = proxy.Params;

client.params#40fdca64 flags:# 
  client_owner_address:string 
  is_test:flags.0?Bool 
  min_proto_version:flags.1?int
  max_proto_version:flags.1?int
  = client.Params;

---functions---

---types---

client.proxyConnectionAuthShort secret_hash:int256 nonce:long = client.ProxyConnectionAuth;
client.proxyConnectionAuthLong  nonce:long = client.ProxyConnectionAuth;

client.authorizationWithProxySuccess signed_payment:proxy.SignedPayment tokens_committed_to_db:long max_tokens:long = client.AuthorizationWithProxy;
client.authorizationWithProxyFailed error_code:int error:string = client.AuthorizationWithProxy;
client.connectedToProxy params:proxy.params client_sc_address:string auth:client.ProxyConnectionAuth
                        signed_payment:proxy.SignedPayment = client.ConnectedToProxy;

client.queryAnswer answer:bytes is_completed:Bool request_id:int256 request_tokens_used:tokensUsed = client.QueryAnswer;
client.queryAnswerError error_code:int error:string request_id:int256 request_tokens_used:tokensUsed = client.QueryAnswer;
client.queryAnswerPart answer:bytes is_completed:Bool request_id:int256 request_tokens_used:tokensUsed = client.QueryAnswerPart;
client.queryAnswerPartError error_code:int error:string request_id:int256 request_tokens_used:tokensUsed = client.QueryAnswerPart;

client.queryFinalInfo flags:# tokens_used:tokensUsed worker_debug:flags.0?string proxy_debug:flags.0?string 
                              proxy_start_time:flags.1?double proxy_end_time:flags.1?double 
                              worker_start_time:flags.1?double worker_end_time:flags.1?double = client.QueryFinalInfo;
client.queryAnswerEx request_id:int256 answer:bytes flags:# final_info:flags.0?client.queryFinalInfo = client.QueryAnswerEx;
client.queryAnswerErrorEx request_id:int256 error_code:int error:string flags:# final_info:flags.0?client.queryFinalInfo = client.QueryAnswerEx;
client.queryAnswerPartEx request_id:int256 answer:bytes flags:# final_info:flags.0?client.queryFinalInfo = client.QueryAnswerEx;

client.refund data:bytes = client.Refund;
client.refundRejected active_queries:long = client.Refund;

client.workerType name:string active_workers:int coefficient_min:int coefficient_bucket50:int coefficient_max:int = client.WorkerType;
client.workerTypes types:(vector client.workerType) = client.WorkerTypes;

client.workerInstanceV2#3ea93d00 flags:# coefficient:int active_requests:int max_active_requests:int = client.WorkerInstanceV2;
client.workerTypeV2 name:string workers:(vector client.workerInstanceV2) = client.WorkerTypeV2;
client.workerTypesV2 types:(vector client.workerTypeV2) = client.WorkerTypesV2;

client.paymentStatus signed_payment:proxy.SignedPayment db_tokens:long max_tokens:long = client.PaymentStatus;

proxy.clientRequestPayment request_id:int256 signed_payment:proxy.SignedPayment db_tokens:long max_tokens:long request_tokens:long = proxy.WorkerRequestPayment;

---functions---

client.connectToProxy params:client.params min_config_version:int = client.ConnectedToProxy;
client.authorizeWithProxyShort data:bytes = client.AuthorizationWithProxy;
client.authorizeWithProxyLong = client.AuthorizationWithProxy;
client.runQuery model_name:string query:bytes max_coefficient:int max_tokens:int timeout:double request_id:int256 min_config_version:int = client.QueryAnswer;
client.runQueryEx#f54cb74b model_name:string query:bytes max_coefficient:int max_tokens:int timeout:double request_id:int256 min_config_version:int 
       flags:# enable_debug:flags.0?Bool public_key:flags.1?int256 = client.QueryAnswerEx;
client.requestRefund = client.Refund;

client.updatePaymentStatus = client.PaymentStatus;

client.getWorkerTypes = client.WorkerTypes;
client.getWorkerTypesV2 = client.WorkerTypesV2;

---types---

proxy.signedPayment data:bytes = proxy.SignedPayment;
proxy.signedPaymentEmpty = proxy.SignedPayment;

proxy.queryAnswer answer:bytes is_completed:Bool request_id:int256 tokens_used:tokensUsed = proxy.QueryAnswer;
proxy.queryAnswerError error_code:int error:string request_id:int256 tokens_used:tokensUsed = proxy.QueryAnswer;
proxy.queryAnswerPart answer:bytes is_completed:Bool request_id:int256 tokens_used:tokensUsed = proxy.QueryAnswerPart;
proxy.queryAnswerPartError error_code:int error:string request_id:int256 tokens_used:tokensUsed = proxy.QueryAnswerPart;

proxy.queryFinalInfo flags:# tokens_used:tokensUsed worker_debug:flags.0?string 
                             worker_start_time:flags.1?double worker_end_time:flags.1?double = proxy.QueryFinalInfo;
proxy.queryAnswerEx request_id:int256 answer:bytes flags:# final_info:flags.0?proxy.queryFinalInfo = proxy.QueryAnswerEx;
proxy.queryAnswerErrorEx request_id:int256 error_code:int error:string flags:# final_info:flags.0?proxy.queryFinalInfo = proxy.QueryAnswerEx;
proxy.queryAnswerPartEx request_id:int256 answer:bytes flags:# final_info:flags.0?proxy.queryFinalInfo = proxy.QueryAnswerEx;

proxy.workerRequestPayment request_id:int256 signed_payment:proxy.SignedPayment db_tokens:long max_tokens:long request_tokens:long request_client_owner:string = proxy.WorkerRequestPayment;

---functions---

proxy.runQuery query:bytes signed_payment:proxy.SignedPayment coefficient:long timeout:double request_id:int256 = proxy.QueryAnswer;
proxy.runQueryEx#c805e7e2 query:bytes signed_payment:proxy.SignedPayment coefficient:long timeout:double request_id:int256 
        flags:# enable_debug:flags.0?Bool private_key:flags.1?int256 = proxy.QueryAnswer;

---types---

worker.enabledDisabled disabled:int = worker.EnabledDisabled;
worker.newCoefficient new_coefficient:int = worker.NewCoefficient;

worker.connectedToProxy params:proxy.params worker_sc_address:string = worker.ConnectedToProxy;
worker.compareBalanceWithProxyResult signed_payment:proxy.SignedPayment tokens_committed_to_db:long max_tokens:long error_code:int = worker.CompareBalanceWithProxyResult;
worker.extendedCompareBalanceWithProxyResult error_code:int = worker.ExtendedCompareBalanceWithProxyResult;
worker.proxyHandshakeCompleted = worker.HandshakeCompleted;

worker.paymentStatus signed_payment:proxy.SignedPayment db_tokens:long max_tokens:long = worker.PaymentStatus;

worker.changedModel = worker.ChangedModel;

---functions---

worker.connectToProxy params:worker.params = worker.ConnectedToProxy;
worker.compareBalanceWithProxy tokens_committed_to_blockchain:long tokens_committed_to_db:long max_tokens:long = worker.CompareBalanceWithProxyResult;
worker.extendedCompareBalanceWithProxy tokens_committed_to_db:long other:bytes = worker.ExtendedCompareBalanceWithProxyResult;
worker.proxyHandshakeComplete is_disabled:Bool = worker.HandshakeCompleted;

worker.updatePaymentStatus = worker.PaymentStatus;
worker.changeModel new_model:string = worker.ChangedModel;

---types---

keyManager.privateKey valid_until_utime:int private_key:int256 = keyManager.PrivateKey;
keyManager.privateKeys keys:(vector keyManager.privateKey) = keyManager.PrivateKeys;

---functions---

keyManager.getWorkerPrivateKeys proto_version:int = keyManager.PrivateKeys;  
keyManager.getProxyPrivateKeys proto_version:int = keyManager.PrivateKeys;  

---types---

http.header name:string value:string = http.Header;
http.response http_version:string status_code:int reason:string headers:(vector http.header) payload:bytes = http.Response;

---functions---

http.request method:string url:string http_version:string headers:(vector http.header) payload:bytes = http.Response;
