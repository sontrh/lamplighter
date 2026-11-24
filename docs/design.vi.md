# Lamplighter: ghi chú thiết kế (tiếng Việt)

Cập nhật 2026-11-24. Đây là ghi chú làm việc của tác giả; tài liệu chính thức sẽ là README tiếng Anh.

## Vấn đề

Gno.land không có bộ hẹn giờ: realm chỉ chạy khi có giao dịch gọi nó. Mọi ứng dụng có hạn chót
(escrow tự giải ngân, hụi xoay vòng, đấu giá đóng phiên) đều phải chờ ai đó gửi giao dịch.
Lamplighter là nơi realm đăng việc cần làm theo lịch; ai chạy việc đến hạn thì được trả thưởng ngay
trong giao dịch đó.

## Thành phần

1. `registry/v2`: realm đăng ký việc, giữ quỹ thưởng, cho bất kỳ ai thực thi việc đến hạn. Có dashboard `Render()`.
2. `taproom/v2`: ví dụ đơn giản nhất, một việc lặp lại (đổi loại bia mỗi 5 phút).
3. `lastorders/v1`: ví dụ có tiền thật chuyển động, một phiên đấu giá tự đóng đúng giờ. Mỗi phiên là một việc
   chạy-một-lần; realm tự nạp quỹ cho việc của mình bằng tiền người bán gửi.
4. `bot/`: bot thực thi bằng Go.

Không làm: mạng keeper có đặt cọc và phạt, token riêng, lịch dạng biểu thức cron.

## API của registry

| Hàm | Ai gọi | Tác dụng |
|---|---|---|
| `Register(name, firstDue, interval, reward, callback) uint64` | realm đã triển khai | Tạo việc. `interval = 0` là chạy một lần. |
| `Fund(id)` | người dùng, kèm `-send` ugnot | Nạp quỹ cho việc. |
| `Credit(id, amount)` | realm, sau khi chuyển coin tới registry | Ghi đúng `amount` coin chưa thuộc việc nào vào việc này. |
| `Exec(id)` | bất kỳ ai | Chạy callback nếu đến hạn và đủ quỹ, trả `reward` cho người gọi. |
| `Cancel(id, refundTo)` | realm chủ việc (kể cả từ chính callback của việc) | Xoá việc, hoàn quỹ còn lại. |
| `DueJobs(afterDue, afterID, limit)` | truy vấn | Các việc chạy được ngay, dạng `id:thưởng:đến-hạn`, việc trễ lâu nhất trước. Đọc tiếp bằng con trỏ. |
| `IsDue`, `Exists`, `Done`, `NextDue`, `Balance`, `Reward`, `Owner`, `Unaccounted`, `Path` | truy vấn | Đọc trạng thái. |

Callback có kiểu `func(realm, uint64) error` và nhận mã việc đang chạy.

## Mô hình tin cậy

- **Registry không có quyền gì trên realm khách.** GnoVM luôn chạy callback với quyền của realm khai báo ra nó.
- **Registry chạy bất kỳ callback nào cho bất kỳ ai.** Vì vậy "người gọi là registry" không chứng minh được gì.
  Callback phải kiểm tra cả hai: người gọi là registry **và** mã việc là của mình. Callback để dạng không export.
  Không bao giờ cấp đặc quyền cho địa chỉ của registry.
- **Người chạy việc đang chạy mã của người khác.** Họ trả đặt cọc lưu trữ cho mọi byte trạng thái mã đó thêm vào.
  Luôn gửi `Exec` với `-max-deposit` thấp.
- **Tiền nạp thuộc về việc.** Chỉ realm chủ việc huỷ được, và nó quyết định phần còn lại về đâu.
- **Không có gì đảm bảo việc sẽ được chạy.** Chỉ có động lực kinh tế. Nhưng `Exec` mở cho mọi người,
  nên chủ việc luôn tự chạy được việc của mình.

## Các quy tắc

- Lịch theo lưới cố định `firstDue + k × interval`. Chạy trễ không làm lệch lưới; lỡ nhiều slot thì chỉ chạy một lần.
- Callback trả lỗi hoặc panic thì cả giao dịch bị huỷ: không trả thưởng, không đổi trạng thái.
- Trạng thái việc được cập nhật trước khi gọi callback; khoá `running` chặn callback gọi ngược vào `Exec` và `Credit`.
- Giới hạn: khoảng lặp 10 giây đến 10 năm; lần chạy đầu không quá 10 năm tới; thưởng tối thiểu 1.000 ugnot;
  tên việc tối đa 80 byte, không có ký tự điều khiển, không toàn ký tự vô hình. Các giới hạn này cũng loại bỏ tràn số.
- Script `maketx run` không đăng ký được việc.
- Việc chạy-một-lần chỉ giữ được đúng một lần thưởng. Sau khi chạy nó ở trạng thái "đã xong" cho đến khi chủ việc `Cancel`.
- `DueJobs` đọc một chỉ mục riêng sắp theo thời điểm đến hạn, nên chi phí truy vấn phụ thuộc số việc trả về
  chứ không phụ thuộc tổng số việc. Dashboard phân trang 20 việc.

## Đặt cọc lưu trữ: ai trả, ai nhận lại

Chain khoá 100 ugnot cho mỗi byte trạng thái, lấy từ người gửi giao dịch làm tăng trạng thái, và trả lại cho
người gửi giao dịch làm giảm trạng thái. Thiết kế registry xoay quanh điều này:

- **Đăng ký việc** tốn nhiều nhất: vài kilobyte, người gửi giao dịch đăng ký trả.
- **Nạp quỹ và chạy việc** gần như không đổi dung lượng (đo được 33 byte khi nạp quỹ cho việc đang cạn).
  Mọi việc luôn nằm trong chỉ mục; đổi trạng thái chỉ là đổi khoá, và hai loại khoá dài bằng nhau.
- **Chỉ `Cancel` mới xoá việc**, và chỉ chủ việc gọi được. Vì vậy đặt cọc quay về phía chủ việc sắp xếp,
  không rơi vào tay người chạy việc. Một lượt chạy chỉ trả đúng mức thưởng.

Hai bản trước làm sai chỗ này, đo được trên chain cục bộ:
- Bản đầu có bảng xếp hạng một dòng cho mỗi người chạy: người mới lỗ 0,17 GNOT ở lần chạy đầu.
- Bản xoá việc chạy-một-lần ngay lúc chạy: người chạy phiên đấu giá nhận 0,72 GNOT đặt cọc của người bán,
  gấp 14 lần mức thưởng 0,05 GNOT.

## Bot

Mỗi vòng (mặc định 15 giây):
1. Lấy giá gas và tài khoản một lần, rồi đọc `DueJobs` theo trang, chọn tối đa `-max-jobs` việc
   (bỏ qua việc đang bị cho "nghỉ" và việc có thưởng không thể đạt `-min-profit`).
2. Với từng việc: ký và **mô phỏng** `Exec` với giới hạn rộng nhất người vận hành cho phép.
3. Đọc event của bản mô phỏng: tiền thật sự chuyển cho mình, đặt cọc bị khoá, tiền hoàn.
4. Ký giao dịch thật với gas vừa đủ (cộng 25%) và **trần đặt cọc chỉ bằng mức mô phỏng cộng một khoản nhỏ**
   (`-deposit-slack`, mặc định 2.000 ugnot). Quyết định dựa trên trường hợp xấu nhất: mất hết trần đặt cọc
   và không nhận được đồng hoàn cọc nào.
5. Sau khi chạy, so kết quả thật với ước tính.

Bảo vệ:
- Việc sẽ lỗi hoặc không đủ lãi: nghỉ 1 phút, lần sau gấp đôi, tối đa 1 giờ. Không mất gì.
- Việc đã làm mất tiền (lỗi ở block thật, hoặc trả ít hơn mô phỏng): nghỉ 10 phút rồi gấp đôi; lần thứ ba thì bỏ hẳn.
- Tổng lỗ chạm `-max-loss` (mặc định 100.000 ugnot) thì bot ngừng gửi giao dịch.
- Kiểm tra chain id với node lúc khởi động. Chuỗi ký tự do chain trả về được làm sạch trước khi in ra log.
- Mật khẩu lấy từ `-password-file` hoặc biến môi trường `LAMPLIGHTER_PASSWORD`, không qua tham số dòng lệnh.

Rủi ro còn lại:
- Hai bot đua một việc: bot về sau mất phí gas. Bot chưa có cơ chế giảm đua.
- Mỗi lần ký tốn khoảng 0,34 giây giải mã khoá, hai lần ký cho mỗi việc.

## Số đo (chain cục bộ, mã v1.8.0-rc.1, giá gas 1 ugnot mỗi 1.000 gas)

| | ugnot |
|---|---|
| Thưởng mỗi lần chạy (taproom và phí đấu giá) | 50.000 |
| Bot chạy việc của taproom lần đầu: phí | 12.112 |
| Bot chạy việc đó: đặt cọc lưu trữ | 0 |
| Bot chạy việc đó: lãi ròng | 37.888 |
| Bot đóng một phiên đấu giá: phí, hoàn cọc, lãi ròng | 14.539, 500, 35.961 |
| Mở một phiên đấu giá: trạng thái tăng | 9.694 byte (khoảng 0,97 GNOT đặt cọc từ người bán) |
| Người bán dọn việc đã xong: trạng thái giảm | 7.252 byte (khoảng 0,73 GNOT về lại người bán) |
| Việc "lỗi ở block thật" làm bot mất | đúng một lần phí, 11.345 |

## Kiểm thử

- `make test`: 29 test của ba realm (registry 21, taproom 5, lastorders 3) và 16 test của bot.
  Mọi test của registry kết thúc bằng kiểm tra bất biến: sổ sách khớp tiền thật, và mỗi việc nằm trong chỉ mục
  dưới đúng một khoá khớp trạng thái của nó.
- `make smoke`: chain cục bộ với giao dịch thật, 59 kiểm tra, gồm:
  - registry và taproom: nạp quỹ, chạy, các trường hợp phải từ chối, script `maketx run` bị từ chối;
  - bot với tư cách người thực thi mới: chạy khô, bỏ qua khi không đủ lãi, chạy thật, ước tính khớp số dư;
  - đấu giá: mở phiên (realm tự nạp quỹ cho việc), trả giá, hoàn tiền người bị vượt, bot đóng phiên,
    tiền thắng về người bán, người bán lấy lại đặt cọc;
  - bot trước việc ác ý (`scripts/testdata/hostile`): việc phình trạng thái bị chặn ở bước mô phỏng;
    việc qua mô phỏng nhưng lỗi ở block thật được ghi nhận đúng chi phí rồi cho nghỉ.
- Chưa kiểm được đầu-cuối: việc trả ít hơn mô phỏng mà không lỗi, ngân sách lỗ `-max-loss`, lệnh cấm sau ba lần lỗ
  (ba thứ này chỉ có test đơn vị).

Ba điểm của môi trường test đơn vị Gno cần nhớ khi đọc test: giao dịch bị huỷ không tự hoàn tác trạng thái;
số dư ngân hàng bị xoá giữa các hàm test trong khi trạng thái realm được giữ lại; và `testing.SetRealm`
đặt trong hàm phụ thì hết hiệu lực khi hàm phụ trả về.

## Rà soát

Mã đã qua bốn lượt rà soát độc lập (realm v1, bot và script, chấm điểm theo tiêu chí hackathon, realm v2 trước khi
gửi lên chain). Các phát hiện đã sửa: mượn danh tính qua callback export, tràn số ở khoảng lặp, script `maketx run`
đăng ký được việc, duyệt danh sách không giới hạn, tiền nạp kẹt vĩnh viễn, bot mang nguyên trần đặt cọc vào giao dịch thật,
bot không nhớ việc xấu, `Credit` bị chặn bằng 1 ugnot lẻ, đặt cọc lưu trữ chảy sai người, script triển khai có thể trả phí hai lần.

## Triển khai lên Onyx

- `scripts/deploy-onyx.sh` gửi các realm rồi nạp 2 GNOT cho việc của taproom. Chạy lại an toàn.
- Onyx lưu mã mới ở trạng thái chờ cho đến khi approver của chain kích hoạt. Trang trạng thái:
  https://gpas.onyx.testnets.gno.land. Ngày 24/11 hàng chờ có 38 gói.
- Khoá ký: `tavern` trong `~/.gnome-tavern-keys` (ngoài repo), là ví nóng chỉ dùng cho testnet.
- Bản hiện tại, gửi ngày 2026-11-24: `registry/v2` (block 1366163), `taproom/v2` (1366165), `lastorders/v1` (1366166).
- Các bản đã gửi và bị thay thế, không dùng và không nạp quỹ:
  - `.../lamplighter/registry` và `.../lamplighter/taproom` (block 1364474, 1364475);
  - `.../lamplighter/registry/v1` và `.../lamplighter/taproom/v1` (block 1364827, 1364828).

## Còn mở

1. Video demo.
2. Lịch dạng cron: gói `p/moul/x/daily/cron` chưa có trên Onyx nên chưa import được.
3. Giảm đua giữa các bot.
4. Một việc tốn vài kilobyte trạng thái (hai nút cây AVL); có thể gọn hơn.
