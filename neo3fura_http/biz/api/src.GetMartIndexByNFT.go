package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"io/ioutil"
	"neo3fura_http/lib/type/h160"
	"neo3fura_http/lib/utils"
	"neo3fura_http/var/stderr"
	"net/http"
	"path/filepath"
	"strconv"
	"time"
)

func (me *T) GetMarketIndexByAsset(args struct {
	MarketHash h160.T
	AssetHash  h160.T
	Filter     map[string]interface{}
	Raw        *map[string]interface{}
}, ret *json.RawMessage) error {

	currentTime := time.Now().UnixNano() / 1e6
	if args.MarketHash.Valid() == false {
		return stderr.ErrInvalidArgs
	}
	if args.AssetHash.Valid() == false {
		return stderr.ErrInvalidArgs
	}

	result := make(map[string]interface{})
	var r1, err = me.Client.QueryAggregate(
		struct {
			Collection string
			Index      string
			Sort       bson.M
			Filter     bson.M
			Pipeline   []bson.M
			Query      []string
		}{
			Collection: "Market",
			Index:      "someindex",
			Sort:       bson.M{},
			Filter:     bson.M{},
			Pipeline: []bson.M{
				bson.M{"$match": bson.M{"asset": args.AssetHash.Val(), "amount": bson.M{"$gt": 0}}},
				bson.M{"$group": bson.M{"_id": "$tokenid"}},
				bson.M{"$count": "count"},
			},

			Query: []string{},
		}, ret)

	if err != nil {
		return err
	}

	result["totalsupply"] = r1[0]["count"]

	//获取上架记录
	r2, err := me.Client.QueryAggregate(
		struct {
			Collection string
			Index      string
			Sort       bson.M
			Filter     bson.M
			Pipeline   []bson.M
			Query      []string
		}{
			Collection: "Market",
			Index:      "someindex",
			Sort:       bson.M{},
			Filter:     bson.M{},
			Pipeline: []bson.M{
				bson.M{"$match": bson.M{"asset": args.AssetHash.Val(), "market": args.MarketHash.Val(), "amount": bson.M{"$gt": 0}}}, //上架（正常状态、过期）:auctor，未领取：bidder
				bson.M{"$project": bson.M{"_id": 1, "asset": 1, "tokenid": 1, "amount": 1, "owner": 1, "market": 1, "difference": bson.M{"$eq": []string{"$owner", "$market"}}, "auctionType": 1, "auctor": 1, "auctionAsset": 1, "auctionAmount": 1, "deadline": 1, "bidder": 1, "bidAmount": 1, "timestamp": 1}},
				bson.M{"$match": bson.M{"difference": true}},
			},
			Query: []string{},
		}, ret)

	if err != nil {
		return err
	}
	owner := make([]map[string]interface{}, 0)
	for _, item := range r2 {
		ba := item["bidAmount"].(primitive.Decimal128).String()
		bidAmount, err2 := strconv.ParseInt(ba, 10, 64)
		if err2 != nil {
			return err
		}
		deadline, _ := item["deadline"].(int64)
		if item["owner"] == item["market"] && deadline > currentTime { //在售
			item["account"] = item["auctor"]
		} else if bidAmount > 0 && deadline < currentTime && item["owner"] == item["market"] { //未领取
			item["account"] = item["bidder"]
		} else if deadline < currentTime && bidAmount == 0 && item["owner"] == item["market"] { //过期
			item["account"] = item["auctor"]
		} else {
			item["account"] = ""
		}
		owner = append(owner, item)
	}

	//二级市场未上架数据
	r3, err := me.Client.QueryAggregate(
		struct {
			Collection string
			Index      string
			Sort       bson.M
			Filter     bson.M
			Pipeline   []bson.M
			Query      []string
		}{
			Collection: "Market",
			Index:      "someindex",
			Sort:       bson.M{},
			Filter:     bson.M{},
			Pipeline: []bson.M{
				bson.M{"$match": bson.M{"asset": args.AssetHash.Val(), "market": primitive.Null{}}}, //上架（正常状态、过期）:auctor，未领取：biddernu
				bson.M{"$group": bson.M{"_id": "$owner",
					"owner":    bson.M{"$last": "$owner"},
					"auctor":   bson.M{"$last": "$auctor"},
					"bidder":   bson.M{"$last": "$bidder"},
					"deadline": bson.M{"$last": "$deadline"},
					"market":   bson.M{"$last": "$market"},
				}},
			},
			Query: []string{},
		}, ret)

	if err != nil {
		return err
	}

	if len(r3) > 0 {

	}
	for _, item := range r3 {
		item["account"] = item["owner"]
		owner = append(owner, item)
	}
	ownerGroup := utils.GroupBy(owner, "account") // owner 分组

	ownerCount := len(ownerGroup)
	result["totalowner"] = ownerCount

	//交易数额
	r4, err := me.Client.QueryAggregate(
		struct {
			Collection string
			Index      string
			Sort       bson.M
			Filter     bson.M
			Pipeline   []bson.M
			Query      []string
		}{
			Collection: "MarketNotification",
			Index:      "someindex",
			Sort:       bson.M{},
			Filter:     bson.M{},
			Pipeline: []bson.M{
				bson.M{"$match": bson.M{"asset": args.AssetHash.Val(), "market": args.MarketHash, "eventname": "Claim"}},
			},
			Query: []string{"extendData"},
		}, ret)

	if err != nil {
		return err
	}

	var txAmount float64
	if len(r4) > 0 {
		for _, item := range r4 {
			extendData := item["extendData"].(string)
			if extendData != "" {
				var data map[string]interface{}
				if err1 := json.Unmarshal([]byte(extendData), &data); err1 == nil {
					auctionAsset := data["auctionAsset"].(string)
					dd, _ := OpenAssetHashFile()
					decimal := dd[auctionAsset]
					if decimal == 0 {
						decimal = 1
					}
					bidAmount, err2 := strconv.ParseInt(data["bidAmount"].(string), 10, 64)
					if err2 != nil {
						return err2
					}

					content, err3 := GetPrice(auctionAsset) //

					p := content[1:2]

					if err3 != nil {
						return err3
					}
					price, err4 := strconv.ParseFloat(p, 64)
					if price == 0 {
						price = 1
					}
					if err4 != nil {
						return err4
					}
					tb := bidAmount / decimal
					txprice := float64(tb) * price

					txAmount += txprice

				} else {
					return err1
				}
			}
		}
	} else {
		txAmount = 0
	}

	result["totaltxamount"] = txAmount
	//地板价
	var limit int64 = 1
	var skip int64 = 0
	r5, err := me.Client.QueryAggregate(
		struct {
			Collection string
			Index      string
			Sort       bson.M
			Filter     bson.M
			Pipeline   []bson.M
			Query      []string
		}{
			Collection: "Market",
			Index:      "someindex",
			Sort:       bson.M{},
			Filter:     bson.M{},
			Pipeline: []bson.M{
				bson.M{"$match": bson.M{"asset": args.AssetHash.Val(), "market": args.MarketHash.Val(), "auctionType": bson.M{"$eq": 1}}},
				//bson.M{"$sort":bson.M{"auctionAmount":1}},

				bson.M{"$skip": skip},
				bson.M{"$limit": limit},
			},

			Query: []string{},
		}, ret)

	if err != nil {
		return err
	}

	if len(r5) > 0 {
		result["auctionAsset"] = r5[0]["auctionAsset"]
		result["auctionAmount"] = r5[0]["auctionAmount"]
	} else {
		result["auctionAsset"] = "——"
		result["auctionAmount"] = "——"
	}

	if err != nil {
		return err
	}
	r, err := json.Marshal(result)
	if err != nil {
		return err
	}
	*ret = json.RawMessage(r)

	return nil
}

func GetPrice(asset string) (string, error) {

	client := &http.Client{}
	reqBody := []byte(`["` + asset + `"]`)
	url := "https://onegate.space/api/quote?convert=usd"
	//str :=[]string{asset}
	req, _ :=
		http.NewRequest("POST", url, bytes.NewBuffer(reqBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "0", stderr.ErrPrice
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)

	if err != nil {
		return "0", stderr.ErrPrice
	}
	return string(body), nil
}

func OpenAssetHashFile() (map[string]int64, error) {
	absPath, _ := filepath.Abs("./assethash.json")

	b, err := ioutil.ReadFile(absPath)
	if err != nil {
		fmt.Print(err)
	}
	whitelist := map[string]int64{}
	err = json.Unmarshal([]byte(string(b)), &whitelist)
	if err != nil {
		panic(err)
	}

	return whitelist, err
}
