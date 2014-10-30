package router

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis"
	"github.com/garyburd/redigo/redis"
	"github.com/juju/errors"
	log "github.com/ngaut/logging"
	"github.com/ngaut/zkhelper"
	"github.com/wandoulabs/codis/pkg/models"
)

var (
	conf       *Conf
	s          *Server
	once       sync.Once
	waitonce   sync.Once
	conn       zkhelper.Conn
	redis1     *miniredis.Miniredis
	redis2     *miniredis.Miniredis
	proxyMutex sync.Mutex
)

func InitEnv() {
	go once.Do(func() {
		conn = zkhelper.NewConn()
		conf = &Conf{
			proxyId:     "proxy_test",
			productName: "test",
			zkAddr:      "localhost:2181",
			f:           func(string) (zkhelper.Conn, error) { return conn, nil },
		}

		//init action path
		prefix := models.GetWatchActionPath(conf.productName)
		err := models.CreateActionRootPath(conn, prefix)
		if err != nil {
			log.Fatal(err)
		}

		//init slot
		err = models.InitSlotSet(conn, conf.productName, 1024)
		if err != nil {
			log.Fatal(err)
		}

		err = models.SetSlotRange(conn, conf.productName, 0, 1023, 1, models.SLOT_STATUS_ONLINE)
		if err != nil {
			log.Fatal(err)
		}

		//init  server group
		g := models.NewServerGroup(conf.productName, 1)
		g.Create(conn)

		redis1, _ := miniredis.Run()
		redis2, _ := miniredis.Run()

		s1 := models.NewServer(models.SERVER_TYPE_MASTER, redis1.Addr())
		s2 := models.NewServer(models.SERVER_TYPE_MASTER, redis2.Addr())

		g.AddServer(conn, s1)
		g.AddServer(conn, s2)
		go func() { //set proxy online
			time.Sleep(5 * time.Second)
			err := models.SetProxyStatus(conn, conf.productName, conf.proxyId, models.PROXY_STATE_ONLINE)
			if err != nil {
				log.Fatal(errors.ErrorStack(err))
			}
			time.Sleep(2 * time.Second)
			proxyMutex.Lock()
			defer proxyMutex.Unlock()
			pi := s.getProxyInfo()
			if pi.State != models.PROXY_STATE_ONLINE {
				log.Fatalf("should be online, we got %s", pi.State)
			}
		}()

		proxyMutex.Lock()
		s = NewServer(":19000", ":11000",
			conf,
		)
		proxyMutex.Unlock()
		s.Run()
	})

	waitonce.Do(func() {
		time.Sleep(10 * time.Second)
	})
}

func TestClusterWork(t *testing.T) {
	InitEnv()
	log.Debug("try to connect")
	c, err := redis.Dial("tcp", "localhost:19000")
	if err != nil {
		t.Fatal(err)
	}

	_, err = c.Do("SET", "foo", "bar")
	if err != nil {
		t.Error(err)
	}

	if got, err := redis.String(c.Do("get", "foo")); err != nil || got != "bar" {
		t.Error("'foo' has the wrong value")
	}

	_, err = c.Do("SET", "bar", "foo")
	if err != nil {
		t.Error(err)
	}

	if got, err := redis.String(c.Do("get", "bar")); err != nil || got != "foo" {
		t.Error("'bar' has the wrong value")
	}
}

func TestMarkOffline(t *testing.T) {
	InitEnv()

	suicide := int64(0)
	proxyMutex.Lock()
	s.OnSuicide = func() error {
		atomic.StoreInt64(&suicide, 1)
		return nil
	}
	proxyMutex.Unlock()

	err := models.SetProxyStatus(conn, conf.productName, conf.proxyId, models.PROXY_STATE_MARK_OFFLINE)
	if err != nil {
		t.Fatal(errors.ErrorStack(err))
	}

	time.Sleep(3 * time.Second)

	if atomic.LoadInt64(&suicide) == 0 {
		t.Error("shoud be suicided")
	}
}
