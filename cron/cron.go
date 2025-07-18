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
	"upay_pro/BSC_USD"
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
// 负责定期检查并处理已过期的未支付订单
type ExpiredOrdersJob struct{}

// Run 实现 cron.Job 接口的 Run 方法，处理过期订单
func (j ExpiredOrdersJob) Run() {
	// 查询已过期的订单
	var orders []sdb.Orders
	if err := sdb.DB.Where("status = ?", sdb.StatusExpired).Find(&orders).Error; err != nil {
		mylog.Logger.Info("查询过期订单失败", zap.Any("err", err))
		return
	}

	if len(orders) == 0 {
		mylog.Logger.Info("没有过期的订单需要处理")
		return
	}

	// 批量删除过期订单
	for _, order := range orders {
		err := sdb.DB.Transaction(func(tx *gorm.DB) error {
			// 删除过期订单
			if err := tx.Delete(&order).Error; err != nil {
				mylog.Logger.Info("删除过期订单失败", zap.Any("err", err))
				return err
			}
			return nil
		})

		if err != nil {
			mylog.Logger.Info("处理过期订单失败", zap.Any("err", err), zap.String("trade_id", order.TradeId))
			continue
		}

		mylog.Logger.Info("订单已删除", zap.String("trade_id", order.TradeId))
	}
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
			{
				var td tron.TransferDetails
				var err error
				// 调用TRON API查询指定时间范围内的转账交易
				td = tron.GetTransactions(v.Token, v.StartTime, v.ExpirationTime)
				if td.TransactionID == "" {
					// 记录第一次查询结果为空
					mylog.Logger.Info("第一个API未查询到交易记录，尝试第二个API")
					// 调用第二个API查询指定时间范围内的转账交易
					td, err = tron.GetTransactionsGrid(v.Token, v.StartTime, v.ExpirationTime)
					if err != nil {
						mylog.Logger.Error("第二个API查询失败", zap.Error(err))
						// 记录第二个API查询失败
						// 跳过本次循环，进入下次循环，也就是对下一个订单进行处理
						continue
					}

				}
				// 验证转账金额是否匹配订单金额且交易ID不为空
				if v.ActualAmount == td.Quant && td.TransactionID != "" {
					// 使用事务更新订单状态
					err := sdb.DB.Transaction(func(tx *gorm.DB) error {
						// 更新订单状态为支付成功
						v.Status = sdb.StatusPaySuccess
						// 记录区块链交易ID
						v.BlockTransactionId = td.TransactionID

						// 保存更新到数据库
						if err := tx.Save(&v).Error; err != nil {
							mylog.Logger.Info("更新数据库表失败", zap.Any("err", err))
							return err
						}
						return nil
					})

					// 事务成功后，异步处理回调通知
					if err == nil {
						go ProcessCallback(v)
					} else {
						mylog.Logger.Info("已经检查到了支付金额，但更新数据库表失败", zap.Any("err", err))
					}
				}
			}
		case "TRX":
			if trx.Start(v) {
				go ProcessCallback(v)
			}
		case "USDT-Polygon":
			if USDT_Polygon.Start(v) {
				go ProcessCallback(v)
			}
		case "USDT-BSC":
			if BSC_USD.Start(v) {
				go ProcessCallback(v)
			}
		default:
			{

				mylog.Logger.Info(fmt.Sprintf("当前订单号为%s的钱包类型%s没有配置对应的查询方法，请联系管理员进行新增", v.TradeId, v.Type))
			}
		}

	}

}

// Start 启动定时任务
// 初始化并启动定时任务调度器，包括USDT支付检查和过期订单处理
func Start() {

	// 如果上一次任务还在运行，新的任务执行时间到了，则等待上一次任务完成后再执行
	c := cron.New(cron.WithChain(cron.DelayIfStillRunning(cron.DefaultLogger)))

	// 每 5 秒执行一次 UsdtRateJob 任务
	_, err := c.AddJob("@every 5s", UsdtCheckJob{})
	if err != nil {
		mylog.Logger.Info("未支付订单检测任务添加失败")
	}
	// 每天凌晨3点执行过期订单清理任务
	_, err = c.AddJob("0 5 * * *", ExpiredOrdersJob{})
	if err != nil {
		mylog.Logger.Info("订单清理任务添加失败")
	}
	// mylog.Logger.Info("订单清理任务已完成")
	// 启动 Cron 调度器
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
	// 解锁钱包地址和金额|| 异步进程解锁钱包地址和金额
	go unlockWalletAddressAndAmount(v)

	// 异步回调

	paymentNotification := dto.PaymentNotification_request{
		TradeID:            v.TradeId,
		OrderID:            v.OrderId,
		Amount:             v.Amount,
		ActualAmount:       v.ActualAmount,
		Token:              v.Token,
		BlockTransactionID: v.BlockTransactionId,
		Status:             v.Status,
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
		ok, err := sendAsyncPost(v.NotifyUrl, paymentNotification)
		if ok == "ok" && err == nil {
			err = sdb.DB.Transaction(func(tx *gorm.DB) error {
				v.CallBackConfirm = sdb.CallBackConfirmOk
				return tx.Save(&v).Error
			})
			if err != nil {
				mylog.Logger.Info("更新回调确认状态失败", zap.Any("err", err))
			} else {
				mylog.Logger.Info("已经确认订单支付成功，并把回调CallBackConfirm设置为1")
			}

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
			if err := sdb.DB.Model(&v).UpdateColumn("callback_num", gorm.Expr("callback_num + ?", 1)).Error; err != nil {
				mylog.Logger.Info("更新回调失败次数失败", zap.Any("err", err))
			}
			// 延迟1秒
			time.Sleep(5 * time.Second)

			// 进入下次循环
			// continue
		}
	}
	// 发送Bark通知|| 异步进程发送通知
	go notification.Bark_Start(v)
	go notification.StartTelegram(v)

}
