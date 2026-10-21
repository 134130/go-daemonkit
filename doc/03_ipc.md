# 03. IPC

구현 우선순위 3. Runtime의 제어 IPC 위에 업무 RPC와 Service 등록 모델을 정한다.
결정 사항 외 세부 항목과 공개 API는 논의용 초안이다.

## 결정 사항

- 로컬 transport는 Linux/macOS의 UDS, Windows의 Named Pipe를 사용한다.
- 그 위에 WebSocket을 사용하고, 메시지는 JSON-RPC 2.0 형식으로 교환한다. `jsonrpc: "2.0"`을 포함한다.
- WebSocket 메시지 하나에 JSON-RPC payload 하나를 담는다.
- 요청 ID로 응답을 연결하며, 같은 연결에서 여러 요청을 병행 처리한다.
- 일회성 호출과 장시간 호출은 같은 요청·응답 계약을 사용한다. 연결은 재사용할 수 있다.
- 구독은 subscription ID로 식별하고 이벤트는 notification으로 전달한다. 취소·구독은 프레임워크 확장 계약으로 정의한다.
- 요청 취소는 대상 요청 ID로 전달하며 다른 요청과 연결을 유지한다. 취소는 협조적 중단이며 롤백을 보장하지 않는다.
- 연결 단절 시 해당 연결의 pending 호출을 실패 처리한다. 결과 불명인 변경 요청은 자동 재실행하지 않는다.
- control과 고용량 stream은 별도 WebSocket 연결로 분리할 수 있다. 연결 구성과 메시지 형식은 독립적으로 다룬다.
- 함께 사용하는 연결은 Handshake에서 같은 `launchNonce`의 데몬인지 확인한다.
- 메시지 크기·동시 요청 수·출력 버퍼를 제한한다. 연결별 끊김과 느린 구독자를 처리한다.

### 접근 제어

- 같은 OS 사용자로 실행되는 프로세스를 신뢰한다. 다른 사용자와 원격 연결은 차단한다.
- Linux/macOS는 사용자 소유 전용 디렉터리 `0700` 안에 경로 기반 UDS를 만들고 소켓 권한을 `0600`으로 제한한다. 디렉터리 소유자와 권한을 검증한다.
- Windows는 현재 사용자 SID로 제한한 명시적 Named Pipe DACL을 적용하고 원격 연결을 거절한다.
- 접근 제어는 transport에서 RPC Handshake 전에 적용한다. 보호 설정·검증 실패 시 데몬 시작을 실패 처리하며 권한을 넓혀 재시도하지 않는다.
- 별도 인증 token이나 인증 RPC를 두지 않는다. Handshake 필드는 대상·호환성 확인용이다.
- 같은 사용자 내부의 악성 프로세스 격리와 관리자 권한 공격 방어는 범위 밖이다.

### 연결 초기화

- 각 연결은 `initialize` 요청 → 서버 응답 → `initialized` 알림 순서로 Handshake한다.
- 요청 필드는 `appID`, `instanceID`, `protocolVersion`, `clientVersion`이다.
- 응답 필드는 `appID`, `instanceID`, `protocolVersion`, `serverVersion`, `launchNonce`이다.
- 양쪽에서 앱·인스턴스와 Protocol 버전을 검증한다. Protocol 버전은 정수이며 정확히 일치해야 한다. 호환 변경은 버전을 유지하고 비호환 변경에서 올린다.
- Binary 버전은 진단용이며 연결 거절 기준으로 사용하지 않는다. `launchNonce`는 실행 세대 식별용이며 인증 수단이 아니다.
- 현재 계약에 capabilities 필드나 기능 협상용 확장 인터페이스를 두지 않는다.
- 서버는 초기화 응답으로 연결 수락과 협상 결과를 전달한다.
- 클라이언트는 협상 결과 적용과 수신 처리 준비를 마친 뒤 `initialized`를 보낸다. 이 알림에는 응답하지 않는다.
- 서버는 `initialized` 수신 이후에만 서버발 notification·request를 전송한다.
- 연결별 수신 준비 상태와 데몬의 Ready 상태는 별개다.
- `initialized`는 개별 이벤트의 수신 확인이나 전달 보장을 의미하지 않는다.
- 클라이언트는 `initialized` 전송 후 일반 RPC를 보낼 수 있다. 서버는 초기화 메시지를 순서대로 처리한 뒤 일반 요청을 병행 실행한다.
- 초기화 전 일반 요청, 중복 초기화, 순서 위반은 연결을 종료한다. 요청에는 오류를 응답하고 알림에는 응답하지 않는다.
- Protocol·대상 불일치는 오류 응답 후 연결을 종료한다. 데몬을 교체하거나 종료하지 않는다.
- 서버는 초기화 deadline 안에 `initialized`가 도착하지 않으면 해당 연결을 종료한다. 클라이언트의 대기 취소·timeout도 해당 연결만 종료한다.
- 함께 사용할 연결의 `launchNonce`가 다르면 해당 연결들을 닫고 다시 연결한다.

### Events

- Raw APIs are `App.Publish(topic, payload)`, `Client.Subscribe(ctx, topic)`, `Subscription.Next(ctx, dst)`, and `Subscription.Close()`. Typed Topic contracts reuse these semantics; see [Developer Experience](04_developer_experience.md).
- Topic은 최대 64자의 이름이며 정확히 일치하는 구독에만 전달한다. 별도 typed Topic이나 범용 내부 Bus는 제공하지 않는다.
- 클라이언트가 subscription ID를 생성하고 로컬 큐를 등록한 다음 `$/subscribe` 요청을 보낸다. Params는 `{subscription, topic}`, 성공 결과는 `{}`다.
- 서버 등록 이후 이벤트를 큐에 넣으며, 등록 응답을 보낸 뒤 이벤트 전송을 시작한다. 등록 전 이벤트는 재전송하지 않는다.
- 이벤트는 `$/event` notification이며 Params는 `{subscription, payload}`다. Overflow 종료는 `{subscription, error: "overflow"}`다.
- 해제는 `$/unsubscribe` notification이며 Params는 `{subscription}`다. 없는 ID의 해제는 성공한다. ID는 연결 내부에서만 유효하다.
- Publish는 Service Startup부터 Shutdown까지 가능하며, Run 외부에서는 ErrEventsClosed를 반환한다. 구독자가 없어도 성공한다.
- Publish는 JSON 직렬화 후 큐에 넣고 소비자를 기다리지 않는다. 동시 Publish는 서버에서 직렬화하며 구독별 순서를 유지한다.
- Subscribe의 ctx는 등록 대기에만, Next의 ctx는 한 번의 읽기 대기에만 적용한다. 구독은 Close 또는 연결 종료까지 유지한다.
- Next는 순차 호출한다. 잘못된 destination pointer는 이벤트를 소비하지 않는다. 디코딩 실패는 해당 이벤트만 소비하고 구독을 유지한다.
- 양쪽의 구독별 큐는 16개로 제한한다. 초과 시 큐를 버리고 해당 구독을 ErrEventOverflow로 종료한다.
- 연결별 구독은 32개, 서버 전체 송신 작업은 256개로 제한한다. 종료 중인 송신 작업도 완료 전까지 한도에 포함한다.
- 각 메시지는 기존 64 KiB 한도를 따른다. 공통 reader는 소비자를 기다리지 않고 구독 큐로 분배한다.
- Close는 반복 호출 가능하며 서버 acknowledgment를 기다리지 않는다. 해제 전송 실패 시 연결을 닫아 서버 자원을 회수한다.
- 소켓 쓰기 실패·timeout은 연결 전체를 종료한다. 해당 연결의 RPC와 다른 구독도 실패한다.
- 연결 종료 시 서버 구독과 송신 작업을 정리한다. 자동 재연결·재구독·재전송은 제공하지 않는다.
- 기존 RPC에 대한 호환 확장이므로 Protocol 버전은 유지한다. 구독 미지원 서버는 method-not-found를 반환한다.

## 미확정 사항

- 기본 연결 개수와 stream 분리 기준.
- Batch 처리 규칙.
- 초기화 timeout 값.
- 각 제한값과 heartbeat·timeout 정책.

## 챙길 내용

- Explicit RPC contracts and handler registration are defined in [Developer Experience](04_developer_experience.md). Lifecycle registration does not expose methods.
- 요청·응답 형식, 오류 표현, 직렬화, 입력 크기 제한과 검증.
- OS별 로컬 소켓·Named Pipe 접근 제어 구현과 검증.
- 요청별 식별자·Deadline·Cancellation. 취소는 서버 작업의 협조적 중단이며 롤백 아님.
- 동시 호출 및 연결 종료 처리. 결과 불명인 변경 요청의 자동 재실행 금지.
- Protocol 호환성 및 알 수 없는 메서드·필드 처리 규칙.
- 종료·업데이트 시 신규 요청 차단, 진행 중 요청 Drain과 시간 제한.

## 첫 완료 기준

- 같은 사용자 연결 허용, 다른 사용자·원격 연결 차단. 보호 설정 실패 시 시작 실패.
- `initialized` 수신 전 서버발 notification·request 전송 금지. 각 연결의 준비 상태를 독립적으로 처리.
- 실제 Service를 클라이언트에서 호출하고 결과·오류 전달.
- 같은 연결에서 요청 하나를 취소해도 다른 요청 정상 완료.
- 잘못된 입력·연결 종료·종료 중 요청을 예측 가능한 오류로 처리.
