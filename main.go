package main

import (
	"database/sql"
	"flag"
	"fmt"
	"io/ioutil"
	"math"
	"os"
	"reflect"
	"regexp"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"

	"github.com/beego/beego/v2/core/logs"
	"github.com/erikdubbelboer/gspt"
	"github.com/internet-dev/db-export-tool/pkg/tools"
)

type workArgsT struct {
	DbType     string // 数据库类型
	Database   string
	DbHost     string
	DbUser     string
	DbPassword string
	DbCharset  string

	DB *sql.DB

	EscapeFunc func(string) string

	Model     string // 导出模式
	Table     string
	Chunk     bool
	Input     string
	Output    string
	SkipField string
	Help      bool
}

const programName = "db-export-tool"

var workArgs workArgsT

func init() {
	flag.StringVar(&workArgs.DbType, "db-type", "mysql", "set db type, support:mysql,postgres")
	flag.StringVar(&workArgs.Database, "db-name", "", "database")
	flag.StringVar(&workArgs.DbHost, "db-host", "127.0.0.1:3306", "set database host")
	flag.StringVar(&workArgs.DbUser, "db-user", "", "database user")
	flag.StringVar(&workArgs.DbPassword, "db-pwd", "", "database password")
	flag.StringVar(&workArgs.DbCharset, "db-charset", "utf8", "charset")

	flag.StringVar(&workArgs.Model, "model", "schema", "set export model, support:schema,data")
	flag.StringVar(&workArgs.Table, "table", "", "databases tables")
	flag.BoolVar(&workArgs.Chunk, "chunk", true, "export all data use chunk")
	flag.StringVar(&workArgs.Input, "input", "", "export query sql filename")
	flag.StringVar(&workArgs.Output, "output", "", "output file")
	flag.StringVar(&workArgs.SkipField, "skip-field", "", "set skip field when create INSERT sql")
	flag.BoolVar(&workArgs.Help, "h", false, "show usage and exit")

	flag.Usage = usage
}

func errMsg(msg string, code int) {
	fmt.Fprintln(os.Stdout, msg)

	if code != 0 {
		os.Exit(code)
	}
}

func usage() {
	fmt.Fprintf(os.Stdout, programName+`
Usage:
  ./%s -h
  ./%s -db-type=mysql,postgres -db-name=db --table=t1,t2...|all -db-host=host -db-user=user -db-pwd=pwd [--output=./output]
  ./%s -db-type=mysql,postgres --model=data -db-host=host -db-user=user -db-pwd=pwd --table=tb --chunk=true|false --input=./input.sql [--skip-field=f1,f2...] [--output=./output.sql]
`, programName, programName, programName)

	flag.PrintDefaults()
	os.Exit(0)
}

func main() {
	flag.Parse()

	if workArgs.Help {
		flag.Usage()
	}

	if len(workArgs.Database) == 0 {
		flag.Usage()
	}

	if workArgs.DbType != "mysql" && workArgs.DbType != "postgres" {
		errMsg("need to set db type: mysql | postgres", 8)
	}

	if workArgs.DbHost == "" {
		errMsg("please set db host", 9)
	}

	if workArgs.DbUser == "" {
		errMsg("please set db user", 10)
	}

	if workArgs.Model != "schema" && workArgs.Model != "data" {
		errMsg(fmt.Sprintf("no support model: %s", workArgs.Model), 11)
	}

	if workArgs.Model == "schema" && len(workArgs.Table) == 0 {
		errMsg("export schema, but no table assign.", 12)
	}

	if workArgs.Model == "data" {
		if workArgs.Chunk == false && len(workArgs.Input) == 0 {
			errMsg("export data, but no sql file assign.", 13)
		}
	}

	if len(workArgs.Table) <= 0 {
		errMsg("please assign table name.", 14)
	}

	// 连接数据库
	var errDB error
	if workArgs.DbType == "mysql" {
		workArgs.EscapeFunc = tools.AddSlashes
		dsn := fmt.Sprintf(`%s:%s@tcp(%s)/%s?charset=%s`, workArgs.DbUser, workArgs.DbPassword, workArgs.DbHost, workArgs.Database, workArgs.DbCharset)
		workArgs.DB, errDB = sql.Open("mysql", dsn)
		if errDB != nil {
			errMsg(fmt.Sprintf("can not connect to mysql, dsn: %s, err: %v", dsn, errDB), 110)
		}
	} else {
		workArgs.EscapeFunc = tools.PgEscape
		dsn := fmt.Sprintf(`postgres://%s:%s@%s/%s`, workArgs.DbUser, workArgs.DbPassword, workArgs.DbHost, workArgs.Database)
		if errDB != nil {
			errMsg(fmt.Sprintf("can not connect to postgres, dsn: %s, err: %v", dsn, errDB), 111)
		}
	}

	errDB = workArgs.DB.Ping()
	if errDB != nil {
		panic(errDB)
	}

	gspt.SetProcTitle(programName)

	doWork(workArgs)

	// 关闭数据库连接
	if workArgs.DB != nil {
		workArgs.DB.Close()
	}
}

func doWork(workArgs workArgsT) {
	var output = os.Stdout
	if len(workArgs.Output) > 0 {
		f, err := os.Create(workArgs.Output)
		if err != nil {
			logs.Error("[doWork] can open file: %s, err: %s", workArgs.Output, err.Error())
			os.Exit(20)
		}
		defer f.Close()

		output = f
	}

	timeNow := time.Now()
	comment := fmt.Sprintf("/* export %s by %s at: %d-%02d-%02d %02d:%02d:%02d */\n\n", workArgs.Model, programName,
		timeNow.Year(), int(timeNow.Month()), timeNow.Day(),
		timeNow.Hour(), timeNow.Minute(), timeNow.Second())
	output.WriteString(comment)

	if workArgs.Model == "schema" {
		doWorkExportSchema(workArgs, output)
	} else {
		doWorkExportData(workArgs, output)
	}
}

func doWorkExportSchema(workArgs workArgsT, output *os.File) {
	logs.Informational("[doWorkExportSchem] start work")

	var tables []string

	if workArgs.Table == "all" {
		querySQL := "SHOW TABLES"
		logs.Debug("[doWorkExportSchem] sql: %s", querySQL)

		rows, err := workArgs.DB.Query(querySQL)
		if err != nil {
			panic(err)
		}

		for rows.Next() {
			cols, _ := rows.Columns()
			colsNum := len(cols)
			refs := make([]interface{}, colsNum)
			for i := range refs {
				var ref interface{}
				refs[i] = &ref
			}
			rows.Scan(refs...)

			for k, _ := range cols {
				val := reflect.Indirect(reflect.ValueOf(refs[k])).Interface()
				tableName := fmt.Sprintf("%s", val)
				tables = append(tables, tableName)
			}
		}
	} else {
		tables = strings.Split(workArgs.Table, ",")
	}
	//logs.Debug("[doWorkExportSchem] tables: %#v\n", tables)

	for _, tbl := range tables {
		addIf := fmt.Sprintf("DROP TABLE IF EXISTS %s;\n", tbl)
		output.WriteString(addIf)

		querySQL := fmt.Sprintf("SHOW CREATE TABLE %s", tbl)
		logs.Debug("[doWorkExportSchem] sql: %s", querySQL)

		var createSQL = ""

		rows, err := workArgs.DB.Query(querySQL)
		if err != nil {
			panic(err)
		}

		for rows.Next() {
			cols, _ := rows.Columns()
			colsNum := len(cols)
			refs := make([]interface{}, colsNum)
			for i := range refs {
				var ref interface{}
				refs[i] = &ref
			}
			rows.Scan(refs...)

			for k, col := range cols {
				logs.Debug("col:", col)
				if col == "Create Table" {
					val := reflect.Indirect(reflect.ValueOf(refs[k])).Interface()
					createSQL = fmt.Sprintf("%s;\n", val)
				}
			}
		}

		re := regexp.MustCompile(`AUTO_INCREMENT=(\d+) `)
		createSQL = re.ReplaceAllString(createSQL, "")

		output.WriteString(createSQL)
		output.WriteString("\n")
	}

	logs.Informational("[doWorkExportSchem] jobs have done.")
}

func doWorkExportData(workArgs workArgsT, output *os.File) {
	logs.Informational("[doWorkExportData] start work")

	// 当 --table=all 时，先查询所有表名，否则直接解析参数中的表名列表
	var tables []string
	if workArgs.Table == "all" {
		querySQL := "SHOW TABLES"
		logs.Debug("[doWorkExportData] sql: %s", querySQL)
		rows, err := workArgs.DB.Query(querySQL)
		if err != nil {
			panic(err)
		}
		defer rows.Close()

		for rows.Next() {
			var tableName string
			if err := rows.Scan(&tableName); err != nil {
				panic(err)
			}
			tables = append(tables, tableName)
		}
	} else {
		tables = strings.Split(workArgs.Table, ",")
	}

	// 对每个表分别导出数据
	for _, table := range tables {
		output.WriteString(fmt.Sprintf("/* export table: %s */\n", table))
		if workArgs.Chunk {
			const chunkSize int64 = 1000

			// 获取该表总行数
			totalSQL := fmt.Sprintf("SELECT COUNT(*) AS total FROM %s", table)
			logs.Debug("[doWorkExportData] sql: %s", totalSQL)
			row := workArgs.DB.QueryRow(totalSQL)
			var total int64
			if err := row.Scan(&total); err != nil {
				panic(err)
			}

			pageTotal := int64(math.Ceil(float64(total) / float64(chunkSize)))
			logs.Debug("[doWorkExportData] table %s total rows: %d, pages: %d", table, total, pageTotal)

			for i := int64(0); i < pageTotal; i++ {
				offset := i * chunkSize
				querySQL := fmt.Sprintf("SELECT * FROM %s LIMIT %d OFFSET %d", table, chunkSize, offset)
				logs.Debug("[doWorkExportData] sql: %s", querySQL)
				output.WriteString(fmt.Sprintf("/** chunk: %d **/\n", i))
				doWorkExportDataUseChunk(workArgs, output, querySQL, table)
			}
		} else {
			// 读取SQL文件（假设SQL文件中包含正确的查询语句，不需要再替换表名）
			sqlBytes, err := ioutil.ReadFile(workArgs.Input)
			if err != nil {
				fmt.Fprintf(os.Stdout, "cannot read sql file:%s, err: %s\n", workArgs.Input, err.Error())
				os.Exit(30)
			}
			querySQL := string(sqlBytes)
			doWorkExportDataUseChunk(workArgs, output, querySQL, table)
		}
	}

	logs.Informational("[doWorkExportData] jobs have done.")
}

func doWorkExportDataUseChunk(workArgs workArgsT, output *os.File, querySQL string, table string) {
	logs.Informational("[doWorkExportDataUseChunk] chunk jobs start.")
	logs.Debug("sql: %s", querySQL)

	rows, err := workArgs.DB.Query(querySQL)
	if err != nil {
		panic(err)
	}
	defer rows.Close()

	var fieldBox []string
	skipFieldBox := make(map[string]bool)
	expSkipField := strings.Split(workArgs.SkipField, ",")
	for _, field := range expSkipField {
		if field != "" {
			skipFieldBox[field] = true
		}
	}

	var columns []string
	var colsNum int
	rowIndex := 0
	for rows.Next() {
		// 首次循环获取字段信息并写入 INSERT 语句
		if rowIndex == 0 {
			columns, _ = rows.Columns()
			for _, col := range columns {
				if skipFieldBox[col] {
					continue
				}
				fieldBox = append(fieldBox, col)
			}
			colsNum = len(columns)
			initSql := fmt.Sprintf("INSERT INTO %s (%s) VALUES\n", table, strings.Join(fieldBox, ", "))
			output.WriteString(initSql)
		} else {
			output.WriteString(",\n")
		}

		// 读取一行数据
		values := make([]string, 0, colsNum)
		refs := make([]interface{}, colsNum)
		for i := range refs {
			var ref interface{}
			refs[i] = &ref
		}
		if err := rows.Scan(refs...); err != nil {
			panic(err)
		}

		// 处理每个字段，跳过不需要的字段
		for k, col := range columns {
			if skipFieldBox[col] {
				continue
			}
			val := reflect.Indirect(reflect.ValueOf(refs[k])).Interface()
			var ve string
			// 如果是 []byte 类型，则转换为字符串
			if b, ok := val.([]byte); ok {
				ve = string(b)
			} else {
				ve = fmt.Sprintf("%v", val)
			}
			escapedVal := workArgs.EscapeFunc(ve)
			values = append(values, fmt.Sprintf("'%s'", escapedVal))
		}
		vSql := fmt.Sprintf("(%s)", strings.Join(values, ", "))
		output.WriteString(vSql)
		rowIndex++
	}

	output.WriteString(";\n\n")
	logs.Informational("[doWorkExportDataUseChunk] chunk jobs have done.")
}
