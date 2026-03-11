package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	Autoprice "upay_pro/AutoPrice"
	"upay_pro/db/sdb"
	"upay_pro/mylog"

	"upay_pro/cron"

	"github.com/fvbock/endless"
	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	"go.uber.org/zap"
)

type User struct {
	UserName string `json:"username" form:"username" validate:"required,min=5,max=12,alphanum"`
	PassWord string `json:"password" form:"password" validate:"required,min=6,max=18,alphanum"`
}

func Start() {
	// 创建一个新的验证器实例
	validate := validator.New()
	r := gin.Default()
	/* 	// 配置 CORS 中间件
	   	r.Use(cors.New(cors.Config{
	   		AllowOrigins:     []string{"*"},                            // 允许的源
	   		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE"}, // 允许的方法
	   		AllowHeaders:     []string{"Origin", "Content-Type"},       // 允许的头
	   		ExposeHeaders:    []string{"Content-Length"},               // 可见的头
	   		AllowCredentials: true,                                     // 允许携带凭据
	   		MaxAge:           10 * time.Minute,                         // 缓存时间
	   	})) */
	// 加载模版
	r.LoadHTMLGlob("static/*.html")
	// 加载静态资源并把原始目录重定向

	r.Static("/css", "./static/css")
	r.Static("/js", "./static/js")
	// 首页路由
	r.GET("/", func(c *gin.Context) {
		c.HTML(200, "index.html", gin.H{})
	})
	// 登录路由组
	{

		r.GET("/login", func(c *gin.Context) {
			c.Header("HX-Redirect", "/login")
			c.HTML(200, "login.html", gin.H{})
		})

		r.POST("/login", func(c *gin.Context) {
			// 创建一个用户结构体
			var user User
			// 绑定请求体
			err := c.ShouldBind(&user)
			if err != nil {
				c.JSON(400, gin.H{
					"message": "参数错误",
				})
				return
			}
			// 验证用户结构体是否符合要求
			err = validate.Struct(user)
			if err != nil {
				c.JSON(400, gin.H{
					"message": err.Error(),
				})
				return
			}
			// 验证用户名密码和数据库是否一致
			var userDB sdb.User
			err = sdb.DB.Where("UserName = ?", user.UserName).First(&userDB).Error
			if err != nil {
				c.JSON(400, gin.H{
					"message": "用户名或密码错误",
				})
				return
			}
			if sdb.VerifyPassword(user.PassWord, userDB.PassWord) {
				// 重定向到后台页面
				// c.Redirect(302, "/admin/")
				c.Header("HX-Redirect", "/admin/")
				// 生成token，并设置到cookie中
				token := GenerateToken()
				// cookie 设置选项 - 使用空字符串让浏览器自动处理域名
				c.SetCookie("token", token, 3600*24, "/", "", false, true)

			} else {
				c.JSON(400, gin.H{
					"message": "用户名或密码错误",
				})
			}

		})
	}
	// 后台路由组
	{
		admin := r.Group("/admin")
		admin.Use(JWTAuthMiddleware())

		admin.GET("/", func(c *gin.Context) {
			c.HTML(200, "admin.html", gin.H{})
		})

		// 用户管理API
		admin.GET("/api/users", func(c *gin.Context) {
			var users []sdb.User
			result := sdb.DB.Find(&users)
			if result.Error != nil {
				c.JSON(http.StatusInternalServerError, gin.H{
					"code": -1,
					"msg":  "获取用户列表失败",
				})
				return
			}
			c.JSON(http.StatusOK, gin.H{
				"code": 0,
				"msg":  "success",
				"data": users,
			})
		})

		// 订单管理API
		admin.GET("/api/orders", func(c *gin.Context) {
			// 获取分页参数
			page := 1
			limit := 10
			if p := c.Query("page"); p != "" {
				if pageNum, err := strconv.Atoi(p); err == nil && pageNum > 0 {
					page = pageNum
				}
			}
			if l := c.Query("limit"); l != "" {
				if limitNum, err := strconv.Atoi(l); err == nil && limitNum > 0 && limitNum <= 100 {
					limit = limitNum
				}
			}

			// 获取搜索参数
			search := c.Query("search")

			// 计算偏移量
			offset := (page - 1) * limit

			// 构建查询条件
			query := sdb.DB.Model(&sdb.Orders{})
			if search != "" {
				// 搜索订单号(TradeId)或商城订单号(OrderId)
				query = query.Where("trade_id LIKE ? OR order_id LIKE ?", "%"+search+"%", "%"+search+"%")
			}

			// 获取总数
			var total int64
			query.Count(&total)

			// 获取订单列表（按ID倒序）
			var orders []sdb.Orders
			result := query.Order("id DESC").Offset(offset).Limit(limit).Find(&orders)
			if result.Error != nil {
				c.JSON(http.StatusInternalServerError, gin.H{
					"code": -1,
					"msg":  "获取订单列表失败",
				})
				return
			}
			c.JSON(http.StatusOK, gin.H{
				"code": 0,
				"msg":  "success",
				"data": gin.H{
					"orders": orders,
					"total":  total,
					"page":   page,
					"limit":  limit,
				},
			})
		})

		// 钱包地址管理API
		admin.GET("/api/wallets", func(c *gin.Context) {
			var wallets []sdb.WalletAddress
			result := sdb.DB.Find(&wallets)
			if result.Error != nil {
				c.JSON(http.StatusInternalServerError, gin.H{
					"code": -1,
					"msg":  "获取钱包地址列表失败",
				})
				return
			}
			c.JSON(http.StatusOK, gin.H{
				"code": 0,
				"msg":  "success",
				"data": wallets,
			})
		})

		// 统计数据API
		admin.GET("/api/stats", func(c *gin.Context) {
			var userCount int64
			var successOrderCount int64
			var walletCount int64

			sdb.DB.Model(&sdb.User{}).Count(&userCount)
			sdb.DB.Model(&sdb.Orders{}).Where("status = ?", sdb.StatusPaySuccess).Count(&successOrderCount)
			sdb.DB.Model(&sdb.WalletAddress{}).Count(&walletCount)

			c.JSON(http.StatusOK, gin.H{
				"code": 0,
				"msg":  "success",
				"data": gin.H{
					"userCount":         userCount,
					"successOrderCount": successOrderCount,
					"walletCount":       walletCount,
				},
			})
		})

		// 修改用户密码
		admin.POST("/api/users/password", func(c *gin.Context) {
			var req struct {
				UserId      int    `json:"userId"`
				NewPassword string `json:"newPassword" validate:"required,min=6,max=18,alphanum"`
			}

			if err := c.ShouldBindJSON(&req); err != nil {
				c.JSON(400, gin.H{"code": 1, "message": "参数错误"})
				return
			}

			/* 	if req.NewPassword == "" {
				c.JSON(400, gin.H{"code": 1, "message": "新密码不能为空"})
				return
			} */
			//  验证参数是否符合要求
			err := validate.Struct(req)
			if err != nil {
				c.JSON(400, gin.H{"code": 1, "message": err.Error()})
				return
			}

			// 对密码加密
			hash, _ := sdb.HashPassword(req.NewPassword)

			// 更新用户密码
			result := sdb.DB.Model(&sdb.User{}).Where("id = ?", req.UserId).Update("PassWord", hash)
			if result.Error != nil {
				c.JSON(500, gin.H{"code": 1, "message": "更新失败"})
				return
			}

			if result.RowsAffected == 0 {
				c.JSON(404, gin.H{"code": 1, "message": "用户不存在"})
				return
			}

			c.JSON(200, gin.H{"code": 0, "message": "密码修改成功"})
		})

		// 添加钱包地址
		admin.POST("/api/wallets", func(c *gin.Context) {
			// 传入的币种和钱包地址和汇率和状态
			var wallet sdb.WalletAddress

			if err := c.ShouldBindJSON(&wallet); err != nil {
				c.JSON(400, gin.H{"code": 1, "message": "参数错误"})
				return
			}

			if wallet.Currency == "" || wallet.Token == "" {
				c.JSON(400, gin.H{"code": 1, "message": "币种和钱包地址不能为空"})
				return
			}

			if wallet.Rate <= 0 {

				c.JSON(400, gin.H{"code": 1, "message": "汇率必须大于0"})
				return
			}

			// // 创建汇率维护表
			// var autoprice sdb.AutoRate

			// autoprice.Currency = wallet.Currency

			if wallet.AutoRate == true {
				mylog.Logger.Info("自动汇率已启用", zap.String("币种", wallet.Currency))
				// 自动汇率是否启用
				wallet.AutoRate = true
				// 设置钱包地址表里面的汇率字段
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

			} else {
				wallet.AutoRate = false
			}
			// 查询数据库中的钱包记录
			var existingWallet sdb.WalletAddress
			if wallet.AutoRate == false {
				// 检查输入的币种的汇率是否存在，如果存在验证输入的汇率和数据库中的汇率是否一致，如果能找到，说明已经存在了，返回错误要求为汇率必须输入为一致；
				// 这里使用Last是为了获取最新的一条记录，因为如果有两条记录，说明之前有过修改，所以需要验证最新的一条记录的汇率是否一致
				if err := sdb.DB.Where("currency = ? ", wallet.Currency).Last(&existingWallet).Error; err == nil {

					/* fmt.Println("existingWallet.Rate:", existingWallet.Rate)
					fmt.Println("wallet.Rate:", wallet.Rate) */

					if wallet.Rate != existingWallet.Rate {
						c.JSON(400, gin.H{"code": 1, "message": fmt.Sprintf("每一个币种的汇率必须一致，你输入的钱包汇率配置错误，请把汇率设置为%v", existingWallet.Rate)})
						return

					}

				}
			}

			// 检查是否已经存在了该币种和地址都存在的记录，如果存在，返回错误，提示钱包地址在该币种下已经存在
			if err := sdb.DB.Where("currency = ? AND token = ?", wallet.Currency, wallet.Token).First(&existingWallet).Error; err == nil {
				c.JSON(400, gin.H{"code": 1, "message": "钱包地址在当前币种中已存在"})
				return
			}

			// 创建钱包地址
			if err := sdb.DB.Create(&wallet).Error; err != nil {
				c.JSON(500, gin.H{"code": 1, "message": "创建失败"})
				return
			}

			c.JSON(200, gin.H{"code": 0, "message": "添加成功", "data": wallet})

		})

		// 编辑钱包地址
		admin.PUT("/api/wallets/:id", func(c *gin.Context) {
			walletId := c.Param("id")
			var wallet sdb.WalletAddress

			if err := c.ShouldBindJSON(&wallet); err != nil {
				c.JSON(400, gin.H{"code": 1, "message": "参数错误"})
				return
			}

			if wallet.Currency == "" || wallet.Token == "" {
				c.JSON(400, gin.H{"code": 1, "message": "币种和钱包地址不能为空"})
				return
			}

			if wallet.Rate <= 0 {
				c.JSON(400, gin.H{"code": 1, "message": "汇率必须大于0"})
				return
			}

			if wallet.AutoRate == true {
				mylog.Logger.Info("自动汇率已启用", zap.String("币种", wallet.Currency))
				// 自动汇率是否启用
				wallet.AutoRate = true
				// 设置钱包地址表里面的汇率字段
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

			} else {
				wallet.AutoRate = false
			}

			/* 	// 检查钱包地址是否已存在（排除当前记录）
			var existingWallet sdb.WalletAddress
			if err := sdb.DB.Where("token = ? AND id != ?", wallet.Token, walletId).First(&existingWallet).Error; err == nil {
				c.JSON(400, gin.H{"code": 1, "message": "钱包地址已存在"})
				return
			} */

			// 更新钱包地址
			result := sdb.DB.Model(&sdb.WalletAddress{}).Where("id = ?", walletId).Updates(map[string]interface{}{
				"Currency": wallet.Currency,
				"Token":    wallet.Token,
				"Rate":     wallet.Rate,
				"Status":   wallet.Status,
				"AutoRate": wallet.AutoRate,
			})

			if result.Error != nil {
				c.JSON(500, gin.H{"code": 1, "message": "更新失败"})
				return
			}

			if result.RowsAffected == 0 {
				c.JSON(404, gin.H{"code": 1, "message": "钱包地址更新失败"})
				return
			}

			c.JSON(200, gin.H{"code": 0, "message": "更新成功"})

		})

		// 删除钱包地址
		admin.DELETE("/api/wallets/:id", func(c *gin.Context) {
			walletId := c.Param("id")

			// 删除钱包地址
			result := sdb.DB.Delete(&sdb.WalletAddress{}, walletId)
			if result.Error != nil {
				c.JSON(500, gin.H{"code": 1, "message": "删除失败"})
				return
			}

			if result.RowsAffected == 0 {
				c.JSON(404, gin.H{"code": 1, "message": "钱包地址不存在"})
				return
			}

			c.JSON(200, gin.H{"code": 0, "message": "删除成功"})

		})

		// 系统设置管理API
		// 获取系统设置
		admin.GET("/api/settings", func(c *gin.Context) {
			var setting sdb.Setting
			result := sdb.DB.First(&setting)
			if result.Error != nil {
				c.JSON(500, gin.H{
					"code": -1,
					"msg":  "获取系统设置失败",
				})
				return
			}
			if result.RowsAffected == 0 {
				c.JSON(500, gin.H{
					"code": -1,
					"msg":  "系统设置不存在",
				})
				return
			}
			c.JSON(http.StatusOK, gin.H{
				"code": 0,
				"msg":  "success",
				"data": setting,
			})
		})

		// 保存系统设置
		admin.POST("/api/settings", func(c *gin.Context) {
			var req map[string]interface{}
			if err := c.ShouldBindJSON(&req); err != nil {
				c.JSON(400, gin.H{"code": 1, "message": "参数错误"})
				return
			}

			// 获取当前设置
			var setting sdb.Setting
			result := sdb.DB.First(&setting)
			if result.Error != nil {
				c.JSON(500, gin.H{"code": 1, "message": "获取当前设置失败"})
				return
			}

			// 更新字段（只更新传入的字段）
			updates := make(map[string]interface{})

			// 基础设置
			if appname, ok := req["appname"]; ok {
				if name, ok := appname.(string); ok && name != "" {
					updates["AppName"] = name
				} else {
					c.JSON(400, gin.H{"code": 1, "message": "应用名称不能为空"})
					return
				}
			}
			if customerservicecontact, ok := req["customerservicecontact"]; ok {
				updates["CustomerServiceContact"] = customerservicecontact
			}
			if appurl, ok := req["appurl"]; ok {
				if url, ok := appurl.(string); ok && url != "" {
					updates["AppUrl"] = url
				} else {
					c.JSON(400, gin.H{"code": 1, "message": "应用地址不能为空"})
					return
				}
			}
			if httpport, ok := req["httpport"]; ok {
				if port, ok := httpport.(float64); ok && port >= 1 && port <= 65535 {
					updates["Httpport"] = int(port)
				} else {
					c.JSON(400, gin.H{"code": 1, "message": "HTTP端口必须在1-65535之间"})
					return
				}
			}
			if secretkey, ok := req["secretkey"]; ok {
				updates["SecretKey"] = secretkey
			}
			if expirationdate, ok := req["expirationdate"]; ok {
				if expiration, ok := expirationdate.(float64); ok && expiration > 0 {
					updates["ExpirationDate"] = time.Duration(int64(expiration))
				} else {
					c.JSON(400, gin.H{"code": 1, "message": "过期时间必须大于0"})
					return
				}
			}

			// Redis设置
			if redishost, ok := req["redishost"]; ok {
				if host, ok := redishost.(string); ok && host != "" {
					updates["Redishost"] = host
				} else {
					c.JSON(400, gin.H{"code": 1, "message": "Redis主机不能为空"})
					return
				}
			}
			if redisport, ok := req["redisport"]; ok {
				if port, ok := redisport.(float64); ok && port >= 1 && port <= 65535 {
					updates["Redisport"] = int(port)
				} else {
					c.JSON(400, gin.H{"code": 1, "message": "Redis端口必须在1-65535之间"})
					return
				}
			}
			if redispasswd, ok := req["redispasswd"]; ok {
				updates["Redispasswd"] = redispasswd
			}
			if redisdb, ok := req["redisdb"]; ok {
				if db, ok := redisdb.(float64); ok && db >= 0 && db <= 15 {
					updates["Redisdb"] = int(db)
				} else {
					c.JSON(400, gin.H{"code": 1, "message": "Redis数据库编号必须在0-15之间"})
					return
				}
			}

			// 通知设置
			if tgbotkey, ok := req["tgbotkey"]; ok {
				updates["Tgbotkey"] = tgbotkey
			}
			if tgchatid, ok := req["tgchatid"]; ok {
				updates["Tgchatid"] = tgchatid
			}
			if barkkey, ok := req["barkkey"]; ok {
				updates["Barkkey"] = barkkey
			}

			// 执行更新
			if len(updates) > 0 {
				result := sdb.DB.Model(&setting).Where("id = ?", setting.ID).Updates(updates)
				if result.Error != nil {
					c.JSON(500, gin.H{"code": 1, "message": "保存失败"})
					return
				}
			}

			c.JSON(200, gin.H{"code": 0, "message": "保存成功"})
		})

		// 手动补单
		admin.POST("/api/manual-complete-order", func(c *gin.Context) {
			var req struct {
				OrderID string `json:"order_id" validate:"required"`
			}
			if err := c.ShouldBindJSON(&req); err != nil {
				c.JSON(400, gin.H{"code": 1, "message": "参数绑定错误"})
				return
			}
			// validate := validator.New()
			// 验证参数是否符合要求
			err := validate.Struct(req)
			if err != nil {
				c.JSON(400, gin.H{"code": 1, "message": "参数验证错误"})
				return
			}
			var order sdb.Orders
			// 通过订单号或者商城订单号查询最新的那条记录
			sdb.DB.Where("order_id = ?", req.OrderID).Or("trade_id = ?", req.OrderID).Order("id DESC").First(&order)
			if order.ID == 0 {
				c.JSON(400, gin.H{"code": 1, "message": "订单不存在"})
				return
			}
			order.Status = sdb.StatusPaySuccess
			result := sdb.DB.Save(&order)
			if result.Error != nil {
				c.JSON(500, gin.H{"code": 1, "message": "保存失败"})
				return
			}
			mylog.Logger.Info("订单已手动完成", zap.Any("order_id", order.OrderId))
			// 异步回调
			go cron.ProcessCallback(order)
			c.JSON(200, gin.H{"code": 0, "message": "订单已手动完成"})
		})

		// API密钥管理API
		// 获取波场和以太坊API密钥
		admin.GET("/api/apikeys", func(c *gin.Context) {
			var apiKey sdb.ApiKey
			result := sdb.DB.First(&apiKey)
			if result.Error != nil {
				c.JSON(500, gin.H{
					"code": -1,
					"msg":  "获取API密钥失败",
				})
				return
			}
			if result.RowsAffected == 0 {
				c.JSON(500, gin.H{
					"code": -1,
					"msg":  "API密钥不存在",
				})
				return
			}
			c.JSON(http.StatusOK, gin.H{
				"code": 0,
				"msg":  "success",
				"data": apiKey,
			})
		})

		// 保存API密钥
		admin.POST("/api/apikeys", func(c *gin.Context) {
			var req map[string]interface{}
			if err := c.ShouldBindJSON(&req); err != nil {
				c.JSON(400, gin.H{"code": 1, "message": "参数错误"})
				return
			}

			// 获取当前API密钥
			var apiKey sdb.ApiKey
			result := sdb.DB.First(&apiKey)
			if result.Error != nil {
				c.JSON(500, gin.H{"code": 1, "message": "获取当前API密钥失败"})
				return
			}

			// 更新字段（只更新传入的字段）
			updates := make(map[string]interface{})

			// API密钥设置
			if tronscan, ok := req["tronscan"]; ok {
				updates["Tronscan"] = tronscan
			}
			if trongrid, ok := req["trongrid"]; ok {
				updates["Trongrid"] = trongrid
			}
			if etherscan, ok := req["etherscan"]; ok {
				updates["Etherscan"] = etherscan
			}

			// 执行更新（更新获取到的apiKey记录）
			if len(updates) > 0 {
				// 更新获取到的apiKey变量对应的记录
				result := sdb.DB.Model(&apiKey).Updates(updates)
				if result.Error != nil {
					c.JSON(500, gin.H{"code": 1, "message": "保存失败"})
					return
				}
				// 检查是否有记录被更新
				if result.RowsAffected == 0 {
					c.JSON(500, gin.H{"code": 1, "message": "没有找到要更新的记录"})
					return
				}
			}

			c.JSON(200, gin.H{"code": 0, "message": "保存成功"})
		})

		// 退出登录路由
		admin.POST("/logout", func(c *gin.Context) {
			// 清除cookie
			c.SetCookie("token", "", -1, "/", "", false, true)
			// 跳转到登录页
			c.JSON(200, gin.H{"code": 0, "message": "退出成功"})
		})

	}

	// 定义订单路由组
	api := r.Group("/api", AuthMiddleware())

	api.POST("/create_order", CreateTransaction)

	// 定义支付路由组
	pay := r.Group("/pay")
	// 返回支付页面【支付页面是静态页面，所以需要返回html文件】
	pay.GET("/checkout-counter/:trade_id", CheckoutCounter)

	// 检查订单状态
	pay.GET("/check-status/:trade_id", CheckOrderStatus)

	// 读取系统设置
	port := sdb.GetSetting().Httpport
	endless.ListenAndServe(fmt.Sprintf(":%d", port), r)
}
