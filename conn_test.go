package sql

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
	"github.com/ory/dockertest"
	"github.com/ory/dockertest/docker"
	"github.com/stretchr/testify/assert"
	"gonum.org/v1/plot/plotter"
	"io/ioutil"
	"log"
	"math"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"text/tabwriter"
	"time"
)

type queryComplexity int

const (
	SimpleComplQuery queryComplexity = 0
	MediumComplQuery = 1
	HardComplQuery   = 2
)

const (
	HardQuery string = "select * from abobd first join abobd second on second.o<5 where first.aa like '%a%' order by first.bb desc, first.aa asc"
	MediumQuery = "select * from abobd where o<50000 order by bb desc, aa asc"
)
const (
	IgorMountPoint string = "/Users/igorvozhga/DIPLOMA/mountDir:/var/lib/mysql"
	MikeMountPoint ="/home/user/go/mounts:/var/lib/mysql"
)

func TestBench(t *testing.T) {
	simplebanch(1, foo)
}
func foo() {
	var complexity queryComplexity
	complexity = HardComplQuery
	var err error
	var dbStd *sql.DB
	testMu.Lock()
	benchTestConfig := sqlConfig
	testMu.Unlock()
	benchTestConfig.DBName = "BigBench"
	dbStd, err = sql.Open(driverName, // Cancelable driver instead of mysql
		benchTestConfig.FormatDSN())
	if err != nil {
		log.Fatal(err)
	}
	queryctx, querycancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer querycancel()
	if complexity == HardComplQuery {
		_, err = dbStd.QueryContext(queryctx, "select * from abobd first join abobd second on second.o<5 where first.aa like '%a%' order by first.bb desc, first.aa asc")
		//167 sec
	} else if complexity == SimpleComplQuery {
		_, err = dbStd.QueryContext(queryctx, "select * from abobd where aa")
		//0.014 sec
	} else if complexity == MediumComplQuery {
		_, err = dbStd.QueryContext(queryctx, "select * from abobd where o<110000 order by bb desc, aa asc")
		//5 sec
	}
	if err != context.DeadlineExceeded && err != nil {
		log.Fatal(err)
	}

}

const driverName = "mysql"

type mySQLProcInfo struct {
	ID      int64   `db:"Id"`
	User    string  `db:"User"`
	Host    string  `db:"Host"`
	DB      string  `db:"db"`
	Command string  `db:"Command"`
	Time    int     `db:"Time"`
	State   string  `db:"State"`
	Info    *string `db:"Info"`
}

type mySQLProcsInfo []mySQLProcInfo

func init() {
	CancelModeUsage = false
	DebugMode = false
}

func helperFullProcessList(db *sql.DB) (mySQLProcsInfo, error) {
	dbx := sqlx.NewDb(db, driverName)
	var procs []mySQLProcInfo
	if err := dbx.Select(&procs, "show full processlist"); err != nil {
		return nil, err
	}
	return procs, nil
}

func (ms mySQLProcsInfo) String() string {
	var buf bytes.Buffer
	w := tabwriter.NewWriter(&buf, 0, 8, 1, '\t', 0)
	fmt.Fprintln(w, "ID\tUser\tHost\tDB\tCommand\tTime\tState\tInfo")
	for _, m := range ms {
		fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n", m.ID, m.User, m.Host, m.DB, m.Command, m.Time,
			m.State, m.Info)
	}
	w.Flush()
	return buf.String()
}

func (ms mySQLProcsInfo) Filter(fns ...func(m mySQLProcInfo) bool) (result mySQLProcsInfo) {
	for _, m := range ms {
		ok := true
		for _, fn := range fns {
			if !fn(m) {
				ok = false
				break
			}
		}
		if ok {
			result = append(result, m)
		}
	}
	return result
}

// nolint:gochecknoglobals
var dockerPool *dockertest.Pool // the connection to docker
// nolint:gochecknoglobals
var systemdb *sql.DB // the connection to the mysql 'system' database
// nolint:gochecknoglobals
var sqlConfig *mysql.Config // the mysql container and config for connecting to other databases
// nolint:gochecknoglobals
var testMu *sync.Mutex // controls access to sqlConfig

func TestMain(m *testing.M) {
	_ = mysql.SetLogger(log.New(ioutil.Discard, "", 0)) // silence mysql logger
	testMu = &sync.Mutex{}

	var err error
	dockerPool, err = dockertest.NewPool("")
	if err != nil {
		log.Fatalf("could not connect to docker: %s", err)
	}
	dockerPool.MaxWait = time.Minute * 2

	runOptions := dockertest.RunOptions{
		Repository: "mysql",
		Tag:        "5.6",
		Env:        []string{"MYSQL_ROOT_PASSWORD=secret"},
		Mounts:     []string{IgorMountPoint},
	}
	mysqlContainer, err := dockerPool.RunWithOptions(&runOptions, func(hostcfg *docker.HostConfig) {
		hostcfg.Memory = 1024 * 1024 * 1024 * 2 //2Gb
	})
	if err != nil {
		log.Fatalf("could not start mysqlContainer: %s", err)
	}
	sqlConfig = &mysql.Config{
		User:                 "root",
		Passwd:               "secret",
		Net:                  "tcp",
		Addr:                 fmt.Sprintf("localhost:%s", mysqlContainer.GetPort("3306/tcp")),
		DBName:               "mysql",
		AllowNativePasswords: true,
	}

	if err = dockerPool.Retry(func() error {
		systemdb, err = sql.Open(driverName, sqlConfig.FormatDSN())
		if err != nil {
			return err
		}
		return systemdb.Ping()
	}); err != nil {
		log.Fatal(err)
	}

	code := m.Run()

	// You can't defer this because os.Exit ignores defer
	if err := dockerPool.Purge(mysqlContainer); err != nil {
		log.Fatalf("Could not purge resource: %s", err)
	}

	os.Exit(code)
}

func TestCancel(t *testing.T) {
	fmt.Println("1")
	var err error
	_, err = systemdb.Exec("create database TestCancel")
	assert.NoError(t, err)

	testMu.Lock()
	testCancelConfig := sqlConfig
	testMu.Unlock()
	testCancelConfig.DBName = "TestCancel"
	var dbStd *sql.DB
	dbStd, err = sql.Open("mysqlc", // Cancelable driver instead of mysql
		testCancelConfig.FormatDSN())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("2")
	procs, err := helperFullProcessList(dbStd)
	assert.NoError(t, err)

	filterDB := func(m mySQLProcInfo) bool { return m.DB == "TestCancel" }
	filterState := func(m mySQLProcInfo) bool { return m.State == "executing" }
	procs = procs.Filter(filterDB, filterState)
	assert.Len(t, procs, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	go func() {
		fmt.Println("3")
		_, err = dbStd.ExecContext(ctx, "select benchmark(9999999999, md5('I like traffic lights'))")
		assert.Equal(t, context.DeadlineExceeded, err)
	}()

	ticker := time.NewTicker(100 * time.Millisecond)
Loop:
	for {
		select {
		case <-ticker.C:
			procs, err := helperFullProcessList(dbStd)
			assert.NoError(t, err)
			procs = procs.Filter(filterDB, filterState)
			assert.Len(t, procs, 1)
		case <-ctx.Done():
			time.Sleep(3000 * time.Millisecond)
			procs, err := helperFullProcessList(dbStd)
			assert.NoError(t, err)
			procs = procs.Filter(filterDB, filterState)
			assert.Len(t, procs, 0)
			break Loop
		}
	}
}

func CreateDatabaseTable(db *sql.DB) {
	var err error
	_, err = db.Exec("DROP TABLE abobd")
	_, err = db.Exec("CREATE TABLE IF NOT EXISTS abobd  (o int AUTO_INCREMENT PRIMARY KEY, aa nvarchar(1025), bb nvarchar(1025), cc nvarchar(1025), dd nvarchar(1025) )")
	fmt.Println("2")
	if err != nil {
		log.Fatal(err)
	}
}

const letterBytes = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890йцукенгшщзхъфывапролджэёячсмитьбюЙЦУКЕНГШЩЗХЪФЫВАПРОЛДЖЭЁЯЧСМИТЬБЮ"

func RandStringBytes() string {
	b := make([]byte, 1024)
	for i := range b {
		b[i] = letterBytes[rand.Int63()%194]
	}
	return string(b)
}

func FillDataBaseTable(db *sql.DB, count int) {
	var tx *sql.Tx
	var err error
	for i := 0; i < count; i++ {
		currentctx, currentcancel := context.WithTimeout(context.Background(), 12*time.Hour)
		defer currentcancel()
		tx, err = db.BeginTx(currentctx, nil)
		if err != nil {
			log.Fatal(err)
		}
		_, err = tx.ExecContext(currentctx, "INSERT INTO abobd(aa, bb, cc, dd) VALUES (?,?,?,?)", RandStringBytes(), RandStringBytes(), RandStringBytes(), RandStringBytes())
		if err != nil {
			log.Fatal(err, tx.Rollback())
		}
		err = tx.Commit()
		if err != nil {
			log.Fatal(err)
		}
		if i%100 == 0 {
			fmt.Println(strconv.Itoa(i))
		}
	}
}

const rowsCount = 400000
const iterationCount = 100
var fakeRows *sql.Rows

func calculationPart(dbStd *sql.DB) plotter.XYs {
	var err error
	var xys plotter.XYs
	var durations = make(chan int64, 120)
	var averageTime int64
	var count=0
	done := make(chan struct{})
	hardTicker := time.NewTicker(5 * time.Second)
	mediumTicker := time.NewTicker(2 * time.Second)
	go func(chan struct{}) {
		for {
			select {
			case <-done:
				return
			case <-hardTicker.C:
				queryctx, querycancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer querycancel()
				fakeRows, err = dbStd.QueryContext(queryctx, HardQuery)
				//assert.Equal(t, context.DeadlineExceeded, err)
				if err != nil && err!=context.DeadlineExceeded {
					log.Fatal("got error in hardquery:",err)
				}
				fmt.Println("hard query done")
			}
		}
	}(done)
	go func(chan int64, chan struct{}) {
		for {
			select {
			case <-done:
				close(durations)
				return
			case <-mediumTicker.C:
				//queryctx, querycancel := context.WithTimeout(context.Background(), 5*time.Minute)
				//defer querycancel()
				start := time.Now()
				fakeRows, err = dbStd.Query(MediumQuery)
				if err != nil {
					log.Fatal("got error in MediumQuery")
				}
				d:=time.Since(start).Milliseconds()
				log.Println("MediumQuery duration: ", d)
				durations<-d
			}
		}
	}(durations, done)

	time.Sleep(2 * time.Minute)
	hardTicker.Stop()
	mediumTicker.Stop()
	done <- struct{}{}
	done <- struct{}{}
	for currentDuration := range durations{
		buff := currentDuration
		averageTime+=buff
		count++
		xys = append(xys,struct{X, Y float64}{float64(count), float64(buff)})
	}
	averageTime=averageTime/int64(math.Max(float64(count), 1))
	fmt.Println("Average MediumQuery duration: ",averageTime)
	return xys
}
func connectToDB() *sql.DB{
	var err error
	var dbStd *sql.DB
	testMu.Lock()
	benchTestConfig := sqlConfig
	testMu.Unlock()
	benchTestConfig.DBName = "BigBench"
	if err != nil {
		log.Fatal(err)
	}
	dbStd, err = sql.Open(driverName, benchTestConfig.FormatDSN())
	if err != nil {
		log.Fatal(err)
	}
	return dbStd
}

func TestDemo(t *testing.T) {
	var xys plotter.XYs
	dbStd := connectToDB()
	xys = calculationPart(dbStd)
	makePlot(xys)
}

func BenchmarkHardQuery(b *testing.B) {
	var err error
	var dbStd *sql.DB
	testMu.Lock()
	benchTestConfig := sqlConfig
	testMu.Unlock()
	benchTestConfig.DBName = "BigBench"
	_, err = systemdb.Exec("create database if not exists " + benchTestConfig.DBName)
	assert.NoError(b, err)
	dbStd, err = sql.Open(driverName, // Cancelable driver instead of mysql
		benchTestConfig.FormatDSN())
	var conn driver.Conn
	conn, err = dbStd.Driver().Open(benchTestConfig.FormatDSN())
	canconn := cancellableMysqlConn{
		conn:         conn,
		killerPool:   dbStd,
		connectionID: "1",
		kto:          100000000 * time.Second,
	}
	nvs := []driver.NamedValue{}
	if err != nil {
		log.Fatal(err)
	}

	procs, err := helperFullProcessList(dbStd)
	assert.NoError(b, err)
	filterDB := func(m mySQLProcInfo) bool { return m.DB == benchTestConfig.DBName }
	filterState := func(m mySQLProcInfo) bool { return m.State == "executing" }
	procs = procs.Filter(filterDB, filterState)
	assert.Len(b, procs, 0)
	b.N = iterationCount
	for i := 0; i < b.N; i++ {
		// go func() {
		queryctx, querycancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer querycancel()
		_, err = canconn.QueryContext(queryctx, "select * from abobd as one left join abobd as two on one.a != two.a left join abobd as three on one.a != three.a left join abobd as four on one.a != four.a left join abobd as five on one.a != five.a", nvs)
		assert.Equal(b, context.DeadlineExceeded, err)
	}
}
func BenchmarkHardQueryDefault(b *testing.B) {
	var err error
	var dbStd *sql.DB
	testMu.Lock()
	benchTestConfig := sqlConfig
	testMu.Unlock()
	benchTestConfig.DBName = "BigBench"
	assert.NoError(b, err)
	dbStd, err = sql.Open(driverName, benchTestConfig.FormatDSN())
	if err != nil {
		log.Fatal(err)
	}
	procs, err := helperFullProcessList(dbStd)
	assert.NoError(b, err)
	filterDB := func(m mySQLProcInfo) bool { return m.DB == benchTestConfig.DBName }
	filterState := func(m mySQLProcInfo) bool { return m.State == "executing" }
	procs = procs.Filter(filterDB, filterState)
	assert.Len(b, procs, 0)
	b.N = iterationCount
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		queryctx, querycancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer querycancel()
		fakeRows, err = dbStd.QueryContext(queryctx, "select * from abobd as one left join abobd as two on one.a != two.a left join abobd as three on one.a != three.a left join abobd as four on one.a != four.a left join abobd as five on one.a != five.a")
		assert.Equal(b, context.DeadlineExceeded, err)
	}
}
func BenchmarkSimpleQueries(b *testing.B) {
	var err error
	var rows *sql.Rows
	_ = rows
	var dbStd *sql.DB
	testMu.Lock()
	benchTestConfig := sqlConfig
	testMu.Unlock()
	benchTestConfig.DBName = "BigBench"
	_, err = systemdb.Exec("create database if not exists " + benchTestConfig.DBName)
	assert.NoError(b, err)
	dbStd, err = sql.Open(driverName, // Cancelable driver instead of mysql
		benchTestConfig.FormatDSN())
	if err != nil {
		log.Fatal(err)
	}
	procs, err := helperFullProcessList(dbStd)
	assert.NoError(b, err)
	filterDB := func(m mySQLProcInfo) bool { return m.DB == benchTestConfig.DBName }
	filterState := func(m mySQLProcInfo) bool { return m.State == "executing" }
	procs = procs.Filter(filterDB, filterState)
	assert.Len(b, procs, 0)
	b.N = iterationCount
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// go func() {
		queryctx, querycancel := context.WithTimeout(context.Background(), 10000*time.Millisecond)
		_ = queryctx
		defer querycancel()
		rows, err = dbStd.QueryContext(queryctx, "select a from abobd where o = 1")
		if err != nil {
			log.Fatal(err)
		}
	}
}
func ShowDatabases() {
	var err error
	var rows *sql.Rows
	rows, err = systemdb.Query("show databases")
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()
	second_params := make([]string, 0)
	for rows.Next() {
		var second string
		if err := rows.Scan(&second); err != nil {
			log.Fatal(err)
		}
		second_params = append(second_params, second)
	}
	log.Println("all the bases")
	log.Println(strings.Join(second_params, " "))
}
func CheckRows(db *sql.DB) int {
	var err error
	var rows *sql.Rows
	queryctx, querycancel := context.WithTimeout(context.Background(), 100000000*time.Millisecond)
	defer querycancel()
	rows, err = db.QueryContext(queryctx, "select count(*) from abobd")
	if err != nil {
		log.Fatal(err)
	}
	for rows.Next() {
		var first int
		if err := rows.Scan(&first); err != nil {
			log.Fatal(err)
		}
		return first
	}
	return 0
}

func TestSimple(t *testing.T) {
	var err error
	var rows *sql.Rows
	_ = rows
	var dbStd *sql.DB
	_ = dbStd
	testMu.Lock()
	benchTestConfig := sqlConfig
	testMu.Unlock()
	benchTestConfig.DBName = "BigBench"
	//_, err = systemdb.Exec("create database if not exists " + benchTestConfig.DBName)
	//assert.NoError(t, err)
	dbStd, err = sql.Open(driverName, // Cancelable driver instead of mysql
		benchTestConfig.FormatDSN())
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("%d", CheckRows(dbStd))
	//CreateDatabaseTable(dbStd)
	//FillDataBaseTable(dbStd, rowsCount)
}
func BenchmarkBench(b *testing.B) {
	var err error
	var dbStd *sql.DB
	testMu.Lock()
	benchTestConfig := sqlConfig
	testMu.Unlock()
	benchTestConfig.DBName = "BigBench"
	_, err = systemdb.Exec("create database if not exists " + benchTestConfig.DBName)
	assert.NoError(b, err)
	dbStd, err = sql.Open(driverName, // Cancelable driver instead of mysql
		benchTestConfig.FormatDSN())
	if err != nil {
		log.Fatal(err)
	}
	//TODO: Посмотреть почему processList ломается:
	/*procs, err := helperFullProcessList(dbStd)
	  assert.NoError(b, err)*/
	// выводит между
	/*filterDB := func(m mySQLProcInfo) bool { return m.DB == benchTestConfig.DBName }
	  filterState := func(m mySQLProcInfo) bool { return m.State == "executing" }
	  procs = procs.Filter(filterDB, filterState)
	  assert.Len(b, procs, 0)*/

	// CreateDatabaseTable(dbStd)

	// FillDataBaseTable(dbStd, rowsCount)

	b.N = iterationCount
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// go func() {
		queryctx, querycancel := context.WithTimeout(context.Background(), 1000*time.Millisecond)
		_ = queryctx
		defer querycancel()
		//log.Printf("%d", CheckRows(dbStd))

		if i%10 == 0 {
			fakeRows, err = dbStd.QueryContext(queryctx, "select count(*) from abobd as one left join abobd as two on one.o != two.o left join abobd as three on one.o != three.o left join abobd as four on one.o != four.o")
			assert.Equal(b, context.DeadlineExceeded, err)
		} else {
			fakeRows, err = dbStd.QueryContext(queryctx, "select a from abobd where o = 1")
			if err != nil {
				log.Fatal(err)
			}
		}
		/*defer rows.Close()
		   first_params := make([]int, 0)
		   second_params := make([]string, 0)
		   for rows.Next() {
		   var first int
		   var second string
		   if err := rows.Scan(&first, &second); err != nil {
		   log.Fatal(err)
		   }
		   first_params = append(first_params, first)
		   second_params = append(second_params, second)
		   }
		   // Check for errors from iterating over rows.
		   if err := rows.Err(); err != nil {
		   log.Fatal(err)
		   }
		   for j := 0; j < len(first_params); j++ {
			   log.Printf("%d — %s \n", first_params[j], second_params[j])
		   }*/

		/*procs, err := helperFullProcessList(dbStd)
		  assert.NoError(b, err)
		  procs = procs.Filter(filterDB, filterState)
		  assert.Len(b, procs, 0)*/
		// }()
	}
}
