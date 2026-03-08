import yliveticker


# this function is called on each ticker update
def on_new_msg(ws, msg):
    print(msg)


yliveticker.YLiveTicker(on_ticker=on_new_msg, ticker_names=["^GSPC", "^IXIC", "^RUT"])

