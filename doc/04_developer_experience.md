# 04. Developer Experience

구현 우선순위 4. Runtime·Process Update·IPC 계약을 실제 앱에서 쓰기 쉽게 만든다.
논의용 초안이며 필요가 확인된 기능부터 추가한다.

## 챙길 내용

- Typed Go Binding: 명시된 RPC 계약에서 생성, 서버·클라이언트 타입 일치 검증.
- Client API: Connect와 AutoStart의 부작용 구분, Close·오류·Context 사용 규칙.
- Events: 구독 해제, 느린 소비자, 버퍼 제한, 연결 복구 시 유실·재전송 정책.
- Cobra 연동: 실행·상태·종료·업데이트 명령 연결. 핵심 패키지 의존 여부 검토.
- Test Harness: 임시 인스턴스·상태 디렉터리, 프로세스 정리, 실패 상황 재현.
- 예제·문서: 최소 앱에서 실행 → 연결 → 호출 → 종료까지 설명.

## 첫 완료 기준

- 최소 예제 앱에서 생성된 Typed Client로 호출 성공.
- 테스트마다 인스턴스 격리, 종료 후 프로세스·자원 누수 없음.
- Events 도입 시 재연결·느린 소비자 처리 정책 검증.

핵심 안정성 테스트는 Runtime 단계부터 작성한다. 재사용 가능한 Harness와 편의 도구만 이 단계에서 정리한다.
