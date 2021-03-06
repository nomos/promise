package promise

import (
	"errors"
	"sync"
	"time"
)

// A Promise is a proxy for a value not necessarily known when
// the promise is created. It allows you to associate handlers
// with an asynchronous action's eventual success value or failure reason.
// This lets asynchronous methods return values like synchronous methods:
// instead of immediately returning the final value, the asynchronous method
// returns a promise to supply the value at some point in the future.
type Promise struct {
	pending bool

	// A function that is passed with the arguments Resolve and reject.
	// The executor function is executed immediately by the Promise implementation,
	// passing Resolve and reject functions (the executor is called
	// before the Promise constructor even returns the created object).
	// The Resolve and reject functions, when called, Resolve or reject
	// the promise, respectively. The executor normally initiates some
	// asynchronous work, and then, once that completes, either calls the
	// Resolve function to Resolve the promise or else rejects it if
	// an error or panic occurred.
	executor func(resolve func(interface{}), reject func(interface{}))

	// Stores the result passed to Resolve()
	result interface{}

	// Stores the error passed to reject()
	err error

	// Mutex protects against data race conditions.
	mutex sync.Mutex

	elapseTime time.Duration

	calTime bool

	// WaitGroup allows to block until all callbacks are executed.
	wg sync.WaitGroup
}

type Timeout struct {
	isClose   bool
	closeChan chan struct{}
}

func (this *Timeout) IsClose() bool {
	return this.closeChan == nil
}

func (this *Timeout) Close() {
	go func() {
		if this.closeChan != nil {
			this.closeChan <- struct{}{}
		}
	}()
}

type Interval struct {
	interval  time.Duration
	ticker    *time.Ticker
	closeChan chan struct{}
	f         func()
}

func (this *Interval) IsClose() bool {
	return this.closeChan == nil
}

func (this *Interval) Close() {
	go func() {
		this.closeChan <- struct{}{}
	}()
}

func (this *Timeout) execute(duration time.Duration, f func()) {
	this.closeChan = make(chan struct{})
	go func() {
		for {
			select {
			case <-this.closeChan:
				return
			case <-time.After(duration):
				f()
				this.isClose = true
				close(this.closeChan)
				this.closeChan = nil
				return
			}
		}
	}()
}

func SetTimeout(duration time.Duration, f func()) *Timeout {
	ret := &Timeout{
		isClose: true,
	}
	ret.execute(duration, f)
	return ret
}

func SetInterval(duration time.Duration, f func()) *Interval {
	ret := &Interval{
		interval:  duration,
		ticker:    time.NewTicker(duration),
		closeChan: nil,
		f:         f,
	}
	go func() {
		for {
			select {
			case <-ret.ticker.C:
				f()
			case <-ret.closeChan:
				close(ret.closeChan)
				return
			}
		}
	}()
	return ret
}

func Await(p *Promise) (interface{}, error) {
	return p.Await()

}

func (promise *Promise) CalTime() *Promise {
	promise.calTime = true
	return promise
}

func (promise *Promise) Elapse() time.Duration {
	return promise.elapseTime
}

// Async instantiates and returns a pointer to a new Promise.
func Async(executor func(resolve func(interface{}), reject func(interface{}))) *Promise {
	var promise = &Promise{
		pending:  true,
		executor: executor,
		result:   nil,
		err:      nil,
		mutex:    sync.Mutex{},
		wg:       sync.WaitGroup{},
	}

	promise.wg.Add(1)

	go func() {
		defer promise.handlePanic()
		promise.executor(promise.Resolve, promise.Reject)
	}()

	return promise
}

func (promise *Promise) Resolve(resolution interface{}) {
	promise.mutex.Lock()

	if !promise.pending {
		promise.mutex.Unlock()
		return
	}

	switch result := resolution.(type) {
	case *Promise:
		flattenedResult, err := result.Await()
		if err != nil {
			promise.mutex.Unlock()
			promise.Reject(err)
			return
		}
		promise.result = flattenedResult
	default:
		promise.result = result
	}
	promise.pending = false

	promise.wg.Done()
	promise.mutex.Unlock()
}

func (promise *Promise) Reject(err interface{}) {
	promise.mutex.Lock()
	defer promise.mutex.Unlock()

	if !promise.pending {
		return
	}
	if err1, ok := err.(error); ok {
		promise.err = err1
	} else {
		promise.err = errors.New(err.(string))
	}
	promise.pending = false

	promise.wg.Done()
}

func (promise *Promise) handlePanic() {
	var r = recover()
	if r != nil {
		if err, ok := r.(error); ok {
			promise.Reject(errors.New(err.Error()))
		} else {
			promise.Reject(errors.New(r.(string)))
		}
	}
}

// Then appends fulfillment and rejection handlers to the promise,
// and returns a new promise resolving to the return value of the called handler.
func (promise *Promise) Then(fulfillment func(data interface{}) interface{}) *Promise {
	return Async(func(resolve func(interface{}), reject func(interface{})) {
		result, err := promise.Await()
		if err != nil {
			reject(err)
			return
		}
		resolve(fulfillment(result))
	})
}

// Catch Appends a rejection handler to the promise,
// and returns a new promise resolving to the return value of the handler.
func (promise *Promise) Catch(rejection func(err error) interface{}) *Promise {
	return Async(func(resolve func(interface{}), reject func(interface{})) {
		result, err := promise.Await()
		if err != nil {
			reject(rejection(err))
			return
		}
		resolve(result)
	})
}

// Await is a blocking function that waits for all callbacks to be executed.
// Returns value and error.
// Call on an already resolved Promise to get its result and error
func (promise *Promise) Await() (interface{}, error) {
	if promise.calTime {
		start:=time.Now()
		promise.wg.Wait()
		promise.elapseTime = time.Now().Sub(start)
		return promise.result, promise.err
	}
	promise.wg.Wait()
	return promise.result, promise.err
}

func (promise *Promise) AsCallback(f func(interface{}, error)) {
	go func() {
		promise.wg.Wait()
		f(promise.result, promise.err)
	}()
}

type resolutionHelper struct {
	index int
	data  interface{}
}

func Each(promises ...*Promise) *Promise {
	return Async(func(resolve func(interface{}), reject func(interface{})) {
		resolutions := make([]interface{}, 0)
		for _, promise := range promises {
			result, err := promise.Await()
			if err != nil {
				reject(err)
				return
			}
			resolutions = append(resolutions, result)
		}
		resolve(resolutions)
	})
}

// All waits for all promises to be resolved, or for any to be rejected.
// If the returned promise resolves, it is resolved with an aggregating array of the values
// from the resolved promises in the same order as defined in the iterable of multiple promises.
// If it rejects, it is rejected with the reason from the first promise in the iterable that was rejected.
func All(promises ...*Promise) *Promise {
	psLen := len(promises)
	if psLen == 0 {
		return Resolve(make([]interface{}, 0))
	}

	return Async(func(resolve func(interface{}), reject func(interface{})) {
		resolutionsChan := make(chan resolutionHelper, psLen)
		errorChan := make(chan error, psLen)

		for index, promise := range promises {
			func(i int) {
				promise.Then(func(data interface{}) interface{} {
					resolutionsChan <- resolutionHelper{i, data}
					return data
				}).Catch(func(err error) interface{} {
					errorChan <- err
					return err
				})
			}(index)
		}

		resolutions := make([]interface{}, psLen)
		for x := 0; x < psLen; x++ {
			select {
			case resolution := <-resolutionsChan:
				resolutions[resolution.index] = resolution.data

			case err := <-errorChan:
				reject(err)
				return
			}
		}
		resolve(resolutions)
	})
}

// Race waits until any of the promises is resolved or rejected.
// If the returned promise resolves, it is resolved with the value of the first promise in the iterable
// that resolved. If it rejects, it is rejected with the reason from the first promise that was rejected.
func Race(promises ...*Promise) *Promise {
	psLen := len(promises)
	if psLen == 0 {
		return Resolve(nil)
	}

	return Async(func(resolve func(interface{}), reject func(interface{})) {
		resolutionsChan := make(chan interface{}, psLen)
		errorChan := make(chan error, psLen)

		for _, promise := range promises {
			promise.Then(func(data interface{}) interface{} {
				resolutionsChan <- data
				return data
			}).Catch(func(err error) interface{} {
				errorChan <- err
				return err
			})
		}

		select {
		case resolution := <-resolutionsChan:
			resolve(resolution)

		case err := <-errorChan:
			reject(err)
		}
	})
}

// AllSettled waits until all promises have settled (each may Resolve, or reject).
// Returns a promise that resolves after all of the given promises have either resolved or rejected,
// with an array of objects that each describe the outcome of each promise.
func AllSettled(promises ...*Promise) *Promise {
	psLen := len(promises)
	if psLen == 0 {
		return Resolve(nil)
	}

	return Async(func(resolve func(interface{}), reject func(interface{})) {
		resolutionsChan := make(chan resolutionHelper, psLen)

		for index, promise := range promises {
			func(i int) {
				promise.Then(func(data interface{}) interface{} {
					resolutionsChan <- resolutionHelper{i, data}
					return data
				}).Catch(func(err error) interface{} {
					resolutionsChan <- resolutionHelper{i, err}
					return err
				})
			}(index)
		}

		resolutions := make([]interface{}, psLen)
		for x := 0; x < psLen; x++ {
			resolution := <-resolutionsChan
			resolutions[resolution.index] = resolution.data
		}
		resolve(resolutions)
	})
}

// Resolve returns a Promise that has been resolved with a given value.
func Resolve(resolution interface{}) *Promise {
	return Async(func(resolve func(interface{}), reject func(interface{})) {
		resolve(resolution)
	})
}

// Reject returns a Promise that has been rejected with a given error.
func Reject(err error) *Promise {
	return Async(func(resolve func(interface{}), reject func(interface{})) {
		reject(err)
	})
}
