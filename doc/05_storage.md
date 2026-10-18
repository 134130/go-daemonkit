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

## 미확정 사항

- OS별 keychain backend와 SQLite driver.
- Transaction API·격리 수준·취소 및 commit 실패 처리.
- 없는 키·키 목록 조회·값 크기 제한의 세부 계약.
- Keychain 없는 환경과 잠긴 keychain 처리.
- Storage 초기화 실패와 Service Readiness의 관계.

## 첫 완료 기준

- 재시작 후 저장값 유지, 인스턴스·namespace 간 키 격리.
- 읽기 snapshot 일관성, 여러 키 변경의 원자성, 동시 수정 시 갱신 유실 방지.
- Transaction 실패·중단 시 부분 변경 노출 금지.
- Keychain 접근 실패 시 오류 반환 및 평문 fallback 없음.
