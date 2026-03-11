package cron

// 设置定义任务检查数据库订单表中有未支付的订单，去请求tron的api查询是否支付成功，如果钱包和金额都正确，则将订单状态改为已支付

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	Autoprice "upay_pro/AutoPrice"
	"upay_pro/BSC_USD"
	"upay_pro/ERC20_USDT"
	"upay_pro/USDC_ArbitrumOne"
	"upay_pro/USDC_BSC"
	"upay_pro/USDC_ERC20"
	"upay_pro/USDC_Polygon"
	"upay_pro/USDT_ArbitrumOne"
	"upay_pro/USDT_Polygon"
	"upay_pro/db/rdb"
	"upay_pro/db/sdb"
	"upay_pro/dto"
	"upay_pro/mylog"
	"upay_pro/notification"
	"upay_pro/tron"
	"upay_pro/trx"

	"github.com/robfig/cron/v3"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// 在文件顶部定义全局HTTP客户端
var httpClient = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
	},
}

// 定义一个任务结构体 UsdtRateJob
// 负责定期检查未支付订单的支付状态，并在支付成功后更新订单状态、发送通知和回调
type UsdtCheckJob struct{}

// ExpiredOrdersJob 处理过期订单的任务结构体
// 负责清理一天前已经过期的订单
type ExpiredOrdersJob struct{}

// Run 实现 cron.Job 接口的 Run 方法，处理过期订单
func (j ExpiredOrdersJob) Run() {
	cutoff := time.Now().Add(-24 * time.Hour).UnixMilli()

	result := sdb.DB.Where("status = ? AND expiration_time <= ?", sdb.StatusExpired, cutoff).Delete(&sdb.Orders{})
	if result.Error != nil {
		mylog.Logger.Info("清理过期订单失败", zap.Any("err", result.Error))
		return
	}

	if result.RowsAffected == 0 {
		mylog.Logger.Info("没有需要清理的过期订单")
		return
	}

	mylog.Logger.Info("清理过期订单完成", zap.Int64("deleted_count", result.RowsAffected))
}

// 定义一个异步请求参数的结构体

/* type PaymentNotification struct {
	TradeID            string  `json:"trade_id"`
	OrderID            string  `json:"order_id"`
	Amount             float64 `json:"amount"`
	ActualAmount       float64 `json:"actual_amount"`
	Token              string  `json:"token"`
	BlockTransactionID string  `json:"block_transaction_id"`
	Signature          string  `json:"signature"`
	Status             int     `json:"status"`
} */

// 实现 cron.Job 接口的 Run 方法
func (j UsdtCheckJob) Run() {
	// 创建一个新的 Cron 调度器
	fmt.Println("任务开启，检查未支付订单")
	// 查询所有未支付状态的订单
	var orders []sdb.Orders //因为可能未支付的订单数量较多所以用切片存储每条订单记录
	if err := sdb.DB.Where("status = ?", sdb.StatusWaitPay).Find(&orders).Error; err != nil {
		mylog.Logger.Info("订单查询失败", zap.Any("err", err))
		return
	}

	// 如果没有未支付订单，直接返回
	if len(orders) == 0 {
		fmt.Println("没有未支付的订单")
		return
	}

	// 遍历每个未支付订单
	for _, v := range orders {
		fmt.Printf("订单ID: %s, 正在查询API\n", v.TradeId)
		switch v.Type {
		case "USDT-TRC20":
			if tron.GetTransactions(v) {
				go ProcessCallback(v)
			} else if tron.GetTransactionsGrid(v) {
				go ProcessCallback(v)
			}
		case "TRX":
			if trx.Start(v) {
				go ProcessCallback(v)
			} else if trx.Start2(v) {
				go ProcessCallback(v)
			}
		case "USDT-Polygon":
			if USDT_Polygon.Start(v) {
				go ProcessCallback(v)
			}
		case "USDT-BSC":
			if BSC_USD.Start(v) {
				go ProcessCallback(v)
			} else if BSC_USD.Start_scan(v) {
				go ProcessCallback(v)

			}
		case "USDT-ERC20":
			if ERC20_USDT.Start(v) {
				go ProcessCallback(v)
			}
		case "USDT-ArbitrumOne":
			if USDT_ArbitrumOne.Start(v) {
				go ProcessCallback(v)
			}
		case "USDC-ERC20":
			if USDC_ERC20.Start(v) {
				go ProcessCallback(v)
			}
		case "USDC-Polygon":
			if USDC_Polygon.Start(v) {
				go ProcessCallback(v)
			}
		case "USDC-BSC":
			if USDC_BSC.Start(v) {
				go ProcessCallback(v)
			} else if USDC_BSC.Start_scan(v) {
				go ProcessCallback(v)
			}
		case "USDC-ArbitrumOne":
			if USDC_ArbitrumOne.Start(v) {
				go ProcessCallback(v)
			}
		default:
			{

				mylog.Logger.Info(fmt.Sprintf("当前订单号为%s的钱包类型%s没有配置对应的查询方法，请联系管理员进行新增", v.TradeId, v.Type))
			}
		}

	}

}

// 自动汇率定时任务

type AutoRate struct{}

func (a AutoRate) Run() {

	var wallets []sdb.WalletAddress

	sdb.DB.Where("AutoRate = ?", true).Find(&wallets)
	mylog.Logger.Info("开始执行自动汇率任务", zap.Int("需要更新的地址数量", len(wallets)))
	for _, wallet := range wallets {
		// 币种
		C := ""
		// 如果order.Currency包含了"USDT"，那么C就等于"USDT"
		switch {
		case strings.Contains(wallet.Currency, "USDT"):
			C = "USDT"
		case strings.Contains(wallet.Currency, "USDC"):
			C = "USDC"
		case strings.Contains(wallet.Currency, "TRX"):
			C = "TRX"
		default:
			mylog.Logger.Error("当前币种将自动设置默认汇率：10，请检查是否错误", zap.String("币种", wallet.Currency))
			wallet.Rate = 10
			sdb.DB.Save(&wallet)
			continue // 跳过API调用，直接处理下一个钱包
		}
		price, err := Autoprice.Start(C)
		if err != nil {
			mylog.Logger.Error("获取自动汇率失败，将设置默认汇率，USDT:7,USDC:7,TRX:2.5", zap.Error(err))
			//将设置默认汇率
			// 优化后的switch语句
			switch C {
			case "USDT", "USDC":
				wallet.Rate = 7
			case "TRX":
				wallet.Rate = 2.5
			default:
				wallet.Rate = 10
			}
		} else {
			wallet.Rate = price
		}

		re := sdb.DB.Save(&wallet)
		if re.Error != nil {
			mylog.Logger.Error("自动汇率更新失败", zap.Error(re.Error))
		}
		mylog.Logger.Info("自动汇率更新成功", zap.String("币种", wallet.Currency), zap.Float64("汇率", wallet.Rate))
	}

}

// Start 启动定时任务
// 初始化并启动定时任务调度器，包括USDT支付检查和过期订单处理
func Start() {

	// 如果上一次任务还在运行，新的任务执行时间到了，则等待上一次任务完成后再执行
	// c := cron.New(cron.WithChain(cron.DelayIfStillRunning(cron.DefaultLogger)))
	// 如果上一次任务还在运行，新的任务执行时间到了，则跳过本次执行
	c := cron.New(cron.WithChain(cron.SkipIfStillRunning(cron.DefaultLogger)))

	// 每 2 秒执行一次 UsdtRateJob 任务
	_, err := c.AddJob("@every 2s", UsdtCheckJob{})
	if err != nil {

		mylog.Logger.Info("未支付订单检测任务添加失败")
	}
	// 每天凌晨1点执行过期订单清理任务，删除一天前已经过期的订单
	_, err = c.AddJob("0 1 * * *", ExpiredOrdersJob{})
	if err != nil {
		mylog.Logger.Info("过期订单清理任务添加失败")
	}

	// 启动 Cron 调度器
	_, err = c.AddJob("@every 10m", AutoRate{})
	if err != nil {
		mylog.Logger.Info("自动汇率任务添加失败")
	}

	c.Start()

	// 保持主程序运行，确保任务执行
	select {}
}

// 发起异步 POST 请求
func sendAsyncPost(url string, notification dto.PaymentNotification_request) (string, error) {
	// 将结构体转换为 JSON 数据
	requestBody, err := json.Marshal(notification)
	if err != nil {
		fmt.Printf("JSON 序列化失败: %v\n", err)
		return "", err
	}

	mylog.Logger.Info("发送异步请求，参数序列化为JSON:", zap.String("url", url), zap.String("body", string(requestBody)))

	// 创建 POST 请求
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(requestBody))
	if err != nil {
		fmt.Printf("创建请求失败: %v\n", err)
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	// 发送请求
	// client := &http.Client{Timeout: 10 * time.Second}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}

	// 读取响应
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		fmt.Println("发送成功，服务器返回 200 OK")

		// 读取服务器响应
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(resp.Body)

		if buf.String() == "ok" || buf.String() == "success" {
			fmt.Println("异步回调发送成功，服务器返回字符串 'ok' 或 'success")
			return "ok", nil

		} else {
			mylog.Logger.Info("异步回调，服务器返回字符串不是 'ok' 或 'success'", zap.String("body", buf.String()))
			return "", errors.New("服务器返回字符串不是 'ok' 或 'success'")
		}

	} else {
		// 读取服务器响应
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(resp.Body)
		mylog.Logger.Info("异步回调发送失败，服务器返回状态码：", zap.Any("resp.StatusCode", resp.StatusCode))
		mylog.Logger.Info("异步回调发送失败，服务器返回内容：", zap.Any("buf.String()", buf.String()))
		return "", fmt.Errorf("异步回调发送失败，服务器返回状态码：%d", resp.StatusCode)
	}

	// 异步回调发送 失败返回{
	/*   "status": "fail",
	     "message": "Invalid signature"
	   } */

}

// 生成签名
func generateSignature(data dto.PaymentNotification_request) string {
	// 创建一个参数数组
	params := []string{
		fmt.Sprintf("trade_id=%s", data.TradeID),
		fmt.Sprintf("order_id=%s", data.OrderID),
		fmt.Sprintf("amount=%g", data.Amount),
		fmt.Sprintf("actual_amount=%g", data.ActualAmount),
		fmt.Sprintf("token=%s", data.Token),
		fmt.Sprintf("block_transaction_id=%s", data.BlockTransactionID),
		fmt.Sprintf("status=%d", data.Status),
	}

	// 创建一个新的切片以保存非空字段
	var filteredParams []string

	// 遍历 params 并过滤空值字段
	for _, param := range params {
		if param == "" {
			continue // 跳过空值
		}
		filteredParams = append(filteredParams, param)
	}

	// 排序参数
	sort.Strings(filteredParams)

	// 使用 strings.Join 连接排序后的参数
	signatureString := strings.Join(filteredParams, "&") + sdb.GetSetting().SecretKey

	// 打印拼接的参数
	mylog.Logger.Info("异步回调的拼接的参数", zap.Any("signatureString", signatureString))

	// 计算 MD5 哈希值
	hash := md5.Sum([]byte(signatureString))
	return hex.EncodeToString(hash[:]) // 转为十六进制字符串
}

// 解锁钱包地址和金额
func unlockWalletAddressAndAmount(v sdb.Orders) {
	// 解锁钱包地址和金额
	address_amount := fmt.Sprintf("%s_%f", v.Token, v.ActualAmount)
	cx := context.Background()
	err := rdb.RDB.Del(cx, address_amount).Err()
	if err != nil {
		mylog.Logger.Info("钱包地址和金额解锁失败", zap.Any("err", err))
		// return err
	}
}

// 异步回调
func ProcessCallback(v sdb.Orders) {
	// 先判断一下异步回调状态，如果已经回调了，则不处理
	if v.CallBackConfirm == sdb.CallBackConfirmOk {
		return
	}

	// 解锁钱包地址和金额|| 异步进程解锁钱包地址和金额
	go unlockWalletAddressAndAmount(v)

	// 获取一下最新的订单记录
	v1 := sdb.GetOrderByOrderId(v.OrderId)

	// 判断一下是否已经支付，没有支付，直接返回，不处理
	if v1.Status != sdb.StatusPaySuccess {
		mylog.Logger.Info("订单未支付，不需要异步回调", zap.Any("订单号：%s", v1.TradeId))
		return
	}

	// 异步回调

	paymentNotification := dto.PaymentNotification_request{
		TradeID:            v1.TradeId,
		OrderID:            v1.OrderId,
		Amount:             v1.Amount,
		ActualAmount:       v1.ActualAmount,
		Token:              v1.Token,
		BlockTransactionID: v1.BlockTransactionId,
		Status:             v1.Status,
	}
	// 这里要判断一下BlockTransactionID的值paymentNotification.BlockTransactionId是否为空，如果为空，就给赋值一个默认值0
	if paymentNotification.BlockTransactionID == "" {
		paymentNotification.BlockTransactionID = "0"
	}
	// 生成签名
	signature := generateSignature(paymentNotification)
	paymentNotification.Signature = signature
	// 异步回调最大次数5次
	mylog.Logger.Info("异步回调的参数", zap.Any("参数", paymentNotification))
	// 使用事务简化回调确认

	for i := 0; i < 5; i++ {
		ok, err := sendAsyncPost(v1.NotifyUrl, paymentNotification)
		if ok == "ok" && err == nil {
			err = sdb.DB.Transaction(func(tx *gorm.DB) error {
				v1.CallBackConfirm = sdb.CallBackConfirmOk
				return tx.Save(v1).Error
			})
			if err != nil {
				mylog.Logger.Info("更新回调确认状态失败", zap.Any("err", err))
			} else {
				mylog.Logger.Info("已经确认订单支付成功，并把回调CallBackConfirm设置为1")
			}
			// 异步回调成功后发送telegram、Bark通知|| 异步进程发送通知
			go notification.Bark_Start(v1)
			go notification.StartTelegram(v1)
			break
		}
		if err != nil {

			mylog.Logger.Info("异步回调失败", zap.Any("err", err))
			// 回调次数+1
			// sdb.DB.Model(&v).Update("callback_num", i+1)
			// sdb.DB.Model(&v).UpdateColumn("callback_num", gorm.Expr("callback_num + 1"))
			// if err := sdb.DB.Model(&v).UpdateColumn("callback_num", gorm.Expr("callback_num + ?", 1)).Error; err != nil {
			// 	mylog.Logger.Info("更新回调失败次数失败", zap.Any("err", err))
			// }
			if err := sdb.DB.Model(v).UpdateColumn("callback_num", gorm.Expr("callback_num + ?", 1)).Error; err != nil {
				mylog.Logger.Info("更新回调失败次数失败", zap.Any("err", err))
			}
			// 延迟5秒
			time.Sleep(5 * time.Second)

			// 进入下次循环
			// continue
		}
	}

}
