# 01. Runtime

구현 우선순위 1. 데몬 실행·제어·시작 보장·실패 복구를 하나의 도메인으로 다룬다.
아래 내용과 API 예시는 논의용 초안이며 확정된 스펙이 아니다.

## 챙길 내용

- 인스턴스 식별: 사용자·앱·인스턴스 범위와 상태 디렉터리.
- 직접 실행·종료: 초기화 실패 시 정리, 정상 종료 시 역순 정리와 시간 제한.
- Single Instance: 실행 중 Lock 유지, 동시 시작 시 프로세스 생성 직렬화.
- 최소 제어 IPC: Handshake, Ready, Status, Stop. 업무 RPC는 별도 도메인.
- Readiness: 초기화 완료 및 요청 수신 가능 상태 확인. Protocol 불일치는 안전하게 거절.
- EnsureRunning: 없으면 시작하고 Ready까지 대기. 대기 시간 제한과 호출자 취소 처리.
- Ownership: PID만 신뢰하지 않으며, 소유권 없는 프로세스 종료·소켓 삭제 금지.
- 실패 복구: Crash, Stale Socket, PID 재사용, 시작 도중 CLI 종료 후 재실행.
- Diagnostics: 상태·시작 실패 이유·로그. OS별 Lock·IPC·프로세스 생성 차이 검증.

## 첫 완료 기준

- CLI 100개 동시 실행에도 데몬 생성 1회, 활성 데몬 최대 1개.
- 초기화 실패 시 자원 정리. Crash 및 시작 요청 CLI 종료 후 다음 실행으로 복구.
- Ready 이전 성공 반환 금지. 비호환 데몬은 교체·종료하지 않고 오류 반환.
- 종료 후 재실행 가능. 다른 프로세스와 인스턴스에 영향 없음.

상시 자동 재시작과 Supervisor는 후속 범위. 우선 다음 호출에서 복구한다.

## API 초안

```go
app := daemonkit.New(daemonkit.Options{
	Name: "myapp",
	Scope: daemonkit.ScopeUser,
	StateDir: "~/.local/share/myapp",
})

app.Register(&WorkspaceService{})

if err := app.Run(); err != nil {
	log.Fatal(err)
}
```
