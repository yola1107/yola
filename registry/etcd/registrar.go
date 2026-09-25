package etcd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"yola/internal/contextwait"

	"github.com/go-kratos/kratos/v3/registry"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	"go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"
)

// ErrInstanceExists 表示同 service/ID 的注册尚未回收。
var ErrInstanceExists = errors.New("etcd: service instance already registered")

// Register 仅创建空的 service/ID key；任何失败都只回收本次申请的 lease。
func (r *Registry) Register(ctx context.Context, service *registry.ServiceInstance) (err error) {
	if err = r.enter(ctx); err != nil {
		return err
	}
	defer func() { <-r.operation }()
	if r.attempted {
		return errors.New("etcd: registration already attempted")
	}
	if service == nil || service.Name == "" || service.ID == "" {
		return errors.New("etcd: service identity is required")
	}
	value, err := json.Marshal(service)
	if err != nil {
		return fmt.Errorf("etcd: encode service: %w", err)
	}
	r.attempted = true
	r.key = fmt.Sprintf("%s/%s/%s", r.namespace, service.Name, service.ID)
	grant, err := r.client.Grant(ctx, 15)
	if err != nil {
		return fmt.Errorf("etcd: grant registration lease: %w", err)
	}
	r.leaseID = grant.ID
	defer func() {
		if err != nil {
			// 注册 context 可能已取消，回收仍使用独立且有界的预算。
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			defer cancel()
			err = errors.Join(err, r.revoke(cleanupCtx))
		}
	}()
	result, err := r.client.Txn(ctx).
		If(clientv3.Compare(clientv3.CreateRevision(r.key), "=", 0)).
		Then(clientv3.OpPut(r.key, string(value), clientv3.WithLease(grant.ID))).Commit()
	if err != nil {
		return fmt.Errorf("etcd: register service: %w", err)
	}
	if !result.Succeeded {
		return ErrInstanceExists
	}
	leaseCtx, cancel := context.WithCancel(r.client.Ctx())
	session, err := concurrency.NewSession(r.client, concurrency.WithLease(grant.ID), concurrency.WithContext(leaseCtx))
	if err != nil {
		cancel()
		return fmt.Errorf("etcd: keep registration alive: %w", err)
	}
	if session == nil {
		cancel()
		return errors.New("etcd: registration lease is unavailable")
	}
	r.cancel = cancel
	r.session = session
	return ctx.Err()
}

// Deregister 仅撤销本 Registry 的 lease，不按 service/ID 删除其他代的记录。
func (r *Registry) Deregister(ctx context.Context, service *registry.ServiceInstance) error {
	if err := r.enter(ctx); err != nil {
		return err
	}
	defer func() { <-r.operation }()
	if r.leaseID == 0 {
		return nil
	}
	if service == nil || r.key != fmt.Sprintf("%s/%s/%s", r.namespace, service.Name, service.ID) {
		return errors.New("etcd: deregistration identity does not match")
	}
	return r.revoke(ctx)
}

func (r *Registry) enter(ctx context.Context) error {
	select {
	case r.operation <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-r.operation
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Registry) revoke(ctx context.Context) error {
	if r.cancel != nil {
		r.cancel()
		if err := contextwait.Done(ctx, r.session.Done()); err != nil {
			return err
		}
	}
	_, err := r.client.Revoke(ctx, r.leaseID)
	if err != nil && !errors.Is(err, rpctypes.ErrLeaseNotFound) {
		return fmt.Errorf("etcd: revoke registration lease: %w", err)
	}
	r.leaseID = 0
	return nil
}
