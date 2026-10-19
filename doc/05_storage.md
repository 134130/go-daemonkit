# 05. Storage

Service에 비밀값 저장소와 일반 데이터 저장소를 제공한다.

## 결정 사항

- Secrets: OS keychain 기반. OAuth token 등 비밀값의 Get / Set / Delete 제공.
- Storage: 필수 제공. SQLite 기반 KV의 Get / Set / Delete / List 제공.
- 앱·인스턴스·namespace별로 키를 격리한다. 값은 `[]byte`이며 직렬화는 호출자가 담당한다.
- 일반 데이터는 OS 표준 state 디렉터리에 저장한다. release 디렉터리와 분리한다.
- 읽기 transaction은 일관된 snapshot을 제공한다.
- 쓰기 transaction은 여러 키의 읽기·검증·변경을 원자적으로 처리한다.
- Secrets와 Storage 간 통합 transaction은 제공하지 않는다.
- Keychain 접근 실패 시 오류를 반환한다. 일반 파일로 암묵적 fallback하지 않는다.
- 비밀값은 로그·진단 응답에 포함하지 않는다.
- 앱 데이터의 schema·migration·버전 호환성은 앱이 관리한다.

## SQLite API

- `app.Storage(namespace)`는 파일 접근 없이 namespace 핸들을 만든다. Service 필드로 명시적으로 주입한다.
- App은 Service Startup 전에 DB를 열고 Shutdown 후 닫는다. 핸들에는 Close를 제공하지 않는다.
- `View(ctx, func(*ReadTx) error)`는 읽기 snapshot을 제공한다. ReadTx는 Get / List를 제공한다.
- `Update(ctx, func(*WriteTx) error)`는 BEGIN IMMEDIATE 이후 callback을 한 번 실행하고 성공 시 commit한다. WriteTx는 Get / List / Set / Delete를 제공한다.
- callback 오류·취소·실패한 쓰기는 rollback한다. panic도 rollback을 시도한 뒤 그대로 전달한다. callback은 자동 재실행하지 않는다.
- commit 결과 또는 rollback 성공을 확인하지 못하면 `ErrCommitUncertain`을 반환할 수 있다. 이 오류는 미저장을 보장하지 않는다.
- callback 이후 Tx 사용은 `ErrTransactionClosed`, 실행 전·종료 후 저장소 사용은 `ErrStorageClosed`다. 중첩 transaction과 Tx의 goroutine 간 공유는 지원하지 않는다.
- 없는 키의 Get은 `ErrNotFound`, Delete는 성공한다. 빈 값은 허용한다.
- 키는 NUL 없는 UTF-8이며 최대 1 KiB, 값은 최대 1 MiB다. List는 prefix와 일치하는 키를 정렬해서 반환하며, 1,000개 초과 시 부분 결과 없이 `ErrStorageLimit`을 반환한다.
- 인스턴스별 `storage.sqlite`를 사용한다. modernc.org/sqlite, WAL, synchronous FULL이며 손상 시 빈 DB로 재생성하지 않는다.

## 미확정 사항

- OS별 keychain backend.
- Keychain 없는 환경과 잠긴 keychain 처리.
- Secrets 초기화와 Service Readiness의 관계.

## 첫 완료 기준

- 재시작 후 저장값 유지, 인스턴스·namespace 간 키 격리.
- 읽기 snapshot 일관성, 여러 키 변경의 원자성, 동시 수정 시 갱신 유실 방지.
- Transaction 실패·중단 시 부분 변경 노출 금지.
- Keychain 접근 실패 시 오류 반환 및 평문 fallback 없음.
