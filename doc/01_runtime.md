# 01. Runtime

구현 우선순위 1. 데몬 실행·제어·시작 보장·실패 복구를 하나의 도메인으로 다룬다.
결정 사항 외 세부 항목과 API 예시는 논의용 초안이다.

## 결정 사항

- 사용자별 데몬. Manager가 필요 시 detached process로 시작하고 Ready까지 대기한다.
- 데몬은 시작한 CLI 종료 후에도 실행을 유지한다.
- CLI와 데몬은 같은 바이너리로 빌드하며 실행 모드로 역할을 구분한다.
- 데몬용 바이너리는 관리 디렉터리에 복사하고 release별로 보관한다.
- 정해진 관리 디렉터리의 release 선택 정보를 통해 실행 파일을 찾는다.
- 실행 시 선택된 release의 실제 경로를 확정한다. 실행 중인 release 파일은 수정하지 않는다.
- 상태·로그·작업 디렉터리는 release 디렉터리 밖에 둔다.
- 인스턴스 상태 경로는 `<state root>/<appID>/<instanceID>`다. Linux/macOS는 `XDG_STATE_HOME`을 사용하며 미설정·빈 값·상대 경로이면 `~/.local/state`를 사용한다. Windows는 `LOCALAPPDATA`를 사용한다.
- 선택된 release와 실행 중인 release를 구분한다.
- 인스턴스는 사용자·앱·인스턴스별 고정 경로로 식별한다. Binary·Protocol 버전은 인스턴스 식별에 포함하지 않는다.
- Start·Stop·Restart·Update는 인스턴스별 operation lock으로 직렬화한다. Start는 Ready 확인 또는 대기 종료까지 lock을 유지한다.
- 실행 세대는 `launchNonce`로 구분한다. 종료·자원 정리 시 PID와 OS process identity, 실행 세대의 소유권을 확인한다.
- Ready는 필수 초기화 완료와 실제 제어 IPC의 호환 Handshake 응답으로 확인한다.
- 필수 초기화를 마친 뒤 성공 Handshake를 허용한다. Manager는 프로세스 생존을 확인하며 Handshake를 재시도한다. 연결 실패만으로 프로세스 종료를 단정하지 않는다.
- 공개 Lifecycle enum과 별도 Status RPC는 현재 범위에 포함하지 않는다.
- `shutdown` RPC 응답은 종료 수락을 의미한다. Manager의 Stop은 해당 실행 세대의 실제 프로세스 종료를 확인한 뒤 성공한다.
- 종료 수락 시 신규 업무 요청을 차단하고 응답 전송을 시도한 뒤 진행 중 요청 대기와 자원 정리를 수행한다. 응답 실패·호출자 연결 단절로 종료를 취소하지 않는다.
- 종료 대기·정리는 유한한 시간 안에 수행한다. 유예 시간 초과 시 Manager는 소유권을 재확인하고 강제 종료한다.
- CLI 취소·종료·Ready timeout은 대기만 끝낸다. 이미 생성된 데몬은 유지하며, timeout·취소는 호출자에게 오류로 반환한다.
- 다음 Start는 기존 프로세스와 시작 상태를 재확인한다. 살아 있으나 준비되지 않은 데몬은 대기하며 중복 생성하지 않는다.
- 데몬 자체의 초기화 실패는 자원 정리 후 종료한다.

## 챙길 내용

- 직접 실행·종료: 초기화 실패 시 정리, 정상 종료 시 역순 정리와 시간 제한.
- Single Instance: 실행 중 Lock 유지, 동시 시작 시 프로세스 생성 직렬화.
- 최소 제어 IPC: Handshake와 `shutdown`. 업무 RPC는 별도 도메인.
- Ownership: PID만 신뢰하지 않으며, 소유권 없는 프로세스 종료·소켓 삭제 금지.
- 실패 복구: Crash, Stale Socket, PID 재사용, 시작 도중 CLI 종료 후 재실행.
- Diagnostics: 상태·시작 실패 이유·로그. OS별 Lock·IPC·프로세스 생성 차이 검증.

## 첫 완료 기준

- CLI 100개 동시 실행에도 데몬 생성 1회, 활성 데몬 최대 1개.
- 초기화 실패 시 자원 정리. Crash 및 시작 요청 CLI 종료 후 다음 실행으로 복구.
- Ready 이전 성공 반환 금지. 비호환 데몬은 교체·종료하지 않고 오류 반환.
- 종료 후 재실행 가능. 다른 프로세스와 인스턴스에 영향 없음.
- CLI 취소·Ready timeout 후 데몬이 유지되며, 다음 호출에서 중복 생성 없이 상태 재확인.

상시 자동 재시작과 Supervisor는 후속 범위. 우선 다음 호출에서 복구한다.
