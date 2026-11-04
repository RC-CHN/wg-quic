package armorbind

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"testing"
	"testing/synctest"
	"time"
)

func admissionTestSession(ctx context.Context, capacity int) *session {
	return &session{
		ctx: ctx, send: make(chan outboundPacket, capacity),
		sendSpace: make(chan struct{}, 1),
	}
}

func admissionTestPacket(size int) outboundPacket {
	return outboundPacket{preparedFrame: make([]byte, size), queuedAt: time.Now()}
}

func TestSendAdmissionResumesWhenCapacityIsReleased(t *testing.T) {
	for _, budgetLimited := range []bool{false, true} {
		name := "packet_limit"
		if budgetLimited {
			name = "byte_limit"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := admissionTestSession(context.Background(), 1)
				if budgetLimited {
					s.sendBudgetBytes.Store(100)
				}
				first := admissionTestPacket(100)
				s.reserveSendBytes(100)
				s.send <- first
				done := make(chan struct{})
				start := time.Now()
				go func() {
					defer close(done)
					var admission sendAdmission
					defer admission.stop()
					if reason, err := s.enqueueData(admissionTestPacket(100), &admission); reason != "" || err != nil {
						t.Errorf("admission after drain: reason=%q err=%v", reason, err)
					}
				}()
				synctest.Wait()
				select {
				case <-done:
					t.Fatal("full queue did not apply backpressure")
				default:
				}
				s.releaseSendBytes(<-s.send)
				<-done
				if time.Since(start) != 0 || len(s.send) != 1 || s.sendBytes.Load() != 100 {
					t.Fatalf("drain did not wake sender promptly: elapsed=%v queued=%d bytes=%d", time.Since(start), len(s.send), s.sendBytes.Load())
				}
				s.releaseSendBytes(<-s.send)
				if s.sendBytes.Load() != 0 {
					t.Fatal("byte reservation leaked")
				}
			})
		})
	}
}

func TestSendAdmissionBatchTimeoutAndCancellation(t *testing.T) {
	for _, budgetLimited := range []bool{false, true} {
		for _, cancelWait := range []bool{false, true} {
			name := "packet_limit"
			if budgetLimited {
				name = "byte_limit"
			}
			if cancelWait {
				name += "/cancel"
			} else {
				name += "/timeout"
			}
			t.Run(name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					s := admissionTestSession(ctx, 1)
					if budgetLimited {
						s.sendBudgetBytes.Store(100)
					}
					s.reserveSendBytes(100)
					s.send <- admissionTestPacket(100)
					done := make(chan struct{})
					start := time.Now()
					go func() {
						defer close(done)
						var admission sendAdmission
						defer admission.stop()
						for range 32 {
							reason, err := s.enqueueData(admissionTestPacket(100), &admission)
							if cancelWait {
								if !errors.Is(err, net.ErrClosed) {
									t.Errorf("canceled send: reason=%q err=%v", reason, err)
								}
							} else {
								want := "send_queue_full"
								if budgetLimited {
									want = "send_queue_budget"
								}
								if reason != want || err != nil {
									t.Errorf("timed out send: reason=%q err=%v", reason, err)
								}
							}
						}
					}()
					synctest.Wait()
					if cancelWait {
						cancel()
					}
					<-done
					wantDelay := sendAdmissionWait
					if cancelWait {
						wantDelay = 0
					}
					if time.Since(start) != wantDelay || s.sendBytes.Load() != 100 || len(s.send) != 1 {
						t.Fatalf("batch exceeded wait or leaked reservation: elapsed=%v bytes=%d queued=%d", time.Since(start), s.sendBytes.Load(), len(s.send))
					}
				})
			})
		}
	}
}

func TestSendAdmissionWakesConcurrentWaiters(t *testing.T) {
	for _, increaseBudget := range []bool{false, true} {
		name := "drain"
		if increaseBudget {
			name = "budget_increase"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := admissionTestSession(context.Background(), 9)
				s.sendBudgetBytes.Store(800)
				s.reserveSendBytes(800)
				s.send <- admissionTestPacket(800)
				done := make(chan struct{}, 8)
				start := time.Now()
				for range 8 {
					go func() {
						var admission sendAdmission
						defer admission.stop()
						if reason, err := s.enqueueData(admissionTestPacket(100), &admission); reason != "" || err != nil {
							t.Errorf("concurrent admission: reason=%q err=%v", reason, err)
						}
						done <- struct{}{}
					}()
				}
				synctest.Wait()
				if increaseBudget {
					s.updateSendBudget(1_000_000, 1_000_000, false)
				} else {
					s.releaseSendBytes(<-s.send)
				}
				for range 8 {
					<-done
				}
				if time.Since(start) != 0 {
					t.Fatal("lost capacity notification left concurrent senders asleep")
				}
				for len(s.send) != 0 {
					s.releaseSendBytes(<-s.send)
				}
				if s.sendBytes.Load() != 0 {
					t.Fatal("concurrent admission leaked byte reservations")
				}
			})
		})
	}
}

func TestSendAdmissionControlAndShutdown(t *testing.T) {
	for _, closeBind := range []bool{false, true} {
		name := "session_retirement"
		if closeBind {
			name = "bind_close"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, closeState := context.WithCancel(context.Background())
				defer closeState()
				sessionCtx, closeSession := context.WithCancel(ctx)
				defer closeSession()
				b := New(DefaultConfig())
				state := &runState{ctx: ctx}
				b.state = state
				s := admissionTestSession(sessionCtx, 1)
				s.state = state
				s.priority = make(chan outboundPacket, 2)
				ep := &Endpoint{owner: b, session: s}
				s.endpoint = ep
				s.reserveSendBytes(100)
				s.send <- admissionTestPacket(100)
				done := make(chan error, 1)
				start := time.Now()
				go func() { done <- b.Send([][]byte{make([]byte, 100)}, ep) }()
				synctest.Wait()
				keepalive := make([]byte, 32)
				binary.LittleEndian.PutUint32(keepalive, 4)
				if err := b.Send([][]byte{keepalive}, ep); err != nil {
					t.Fatal(err)
				}
				if len(s.priority) != 2 || time.Since(start) != 0 {
					t.Fatal("control traffic waited behind blocked data admission")
				}
				if closeBind {
					closeState()
				} else {
					closeSession()
				}
				err := <-done
				if closeBind && !errors.Is(err, net.ErrClosed) {
					t.Fatalf("bind close did not cancel data send: %v", err)
				}
				if !closeBind && (err != nil || s.stats.queueDrops.Load() != 1) {
					t.Fatalf("session retirement must drop silently while bind reconnects: err=%v drops=%d", err, s.stats.queueDrops.Load())
				}
				if time.Since(start) != 0 || s.sendBytes.Load() != 100 {
					t.Fatalf("canceled send waited or leaked its reservation: elapsed=%v bytes=%d", time.Since(start), s.sendBytes.Load())
				}
			})
		})
	}
}

func TestBindSendSharesAdmissionDeadlineAcrossBatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		b := New(DefaultConfig())
		state := &runState{ctx: ctx}
		b.state = state
		s := admissionTestSession(ctx, 1)
		s.state = state
		s.priority = make(chan outboundPacket, 2)
		ep := &Endpoint{owner: b, session: s}
		s.endpoint = ep
		s.reserveSendBytes(100)
		s.send <- admissionTestPacket(100)
		var batch [][]byte
		for range b.BatchSize() {
			batch = append(batch, make([]byte, 100))
		}
		keepalive := make([]byte, 32)
		binary.LittleEndian.PutUint32(keepalive, 4)
		batch = append(batch, keepalive)
		start := time.Now()
		if err := b.Send(batch, ep); err != nil {
			t.Fatal(err)
		}
		if time.Since(start) != sendAdmissionWait {
			t.Fatalf("batch multiplied the admission timeout: %v", time.Since(start))
		}
		if s.stats.queueDrops.Load() != uint64(b.BatchSize()) || s.sendBytes.Load() != 100 {
			t.Fatalf("batch rejection accounting: drops=%d bytes=%d", s.stats.queueDrops.Load(), s.sendBytes.Load())
		}
		if len(s.priority) != 2 {
			t.Fatal("expired data deadline rejected control packets in the same batch")
		}
	})
}
