package bbgo

import (
	"context"

	"github.com/c9s/bbgo/pkg/service"
	"github.com/sirupsen/logrus"
)

const IsolationContextKey = "bbgo"

var defaultIsolation = NewDefaultIsolation()

type Isolation struct {
	gracefulShutdown         GracefulShutdown
	persistenceServiceFacade *service.PersistenceServiceFacade
	logger                   logrus.FieldLogger
}

func NewDefaultIsolation() *Isolation {
	return &Isolation{
		gracefulShutdown:         GracefulShutdown{},
		persistenceServiceFacade: defaultPersistenceServiceFacade,
	}
}

func NewIsolation(persistenceFacade *service.PersistenceServiceFacade) *Isolation {
	return &Isolation{
		gracefulShutdown:         GracefulShutdown{},
		persistenceServiceFacade: persistenceFacade,
	}
}

func (i *Isolation) SetLogger(logger logrus.FieldLogger) {
	i.logger = logger
}

func (i *Isolation) GetLogger() logrus.FieldLogger {
	if i.logger == nil {
		return logrus.StandardLogger()
	}
	return i.logger
}

func GetIsolationFromContext(ctx context.Context) *Isolation {
	isolatedContext, ok := ctx.Value(IsolationContextKey).(*Isolation)
	if ok {
		return isolatedContext
	}

	return defaultIsolation
}

// NewTodoContextWithExistingIsolation creates a new context object with the existing isolation of the parent context.
func NewTodoContextWithExistingIsolation(parent context.Context) context.Context {
	isolatedContext := GetIsolationFromContext(parent)
	todo := context.WithValue(context.TODO(), IsolationContextKey, isolatedContext)
	return todo
}

// NewContextWithIsolation creates a new context from the parent context with a custom isolation
func NewContextWithIsolation(parent context.Context, isolation *Isolation) context.Context {
	return context.WithValue(parent, IsolationContextKey, isolation)
}

// NewContextWithDefaultIsolation creates a new context from the parent context with a default isolation
func NewContextWithDefaultIsolation(parent context.Context) context.Context {
	return context.WithValue(parent, IsolationContextKey, defaultIsolation)
}
