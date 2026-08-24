def execute(data):
    ok = data["signature_valid"] and data["signed_chain"] != 0
    return {"authorized": ok}
